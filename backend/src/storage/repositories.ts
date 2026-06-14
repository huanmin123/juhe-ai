import type { DatabaseSync } from 'node:sqlite'

import type { AccountClientCompatibility, AccountGroupBindStatus, AccountStatus, AccountSummary, AccountTrafficMigrationSourceStatus, AccountType, AccountUsageStatsOverview, AccountUsageStatsRange, AccountUsageSummary, AuthorizationStatus, GroupSummary, ResourceAuthorizationListResult, ResourceAuthorizationResourceType, ResourceAuthorizationSourceStatus, ResourceAuthorizationSourceType, ResourceAuthorizationSummary, ResourceAuthorizationUsageDetail } from '../domain/types.js'
import { normalizeOpenAIAccountClientCompatibility } from '../domain/account-client-compatibility.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE, isGptVendorCode } from '../domain/provider-protocol.js'
export type { GroupOptionSummary } from '../domain/types.js'
import { accountSummaryWithEffectiveAvailability } from '../domain/account-effective-availability.js'
import { groupSchedulingPolicyJson, normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import { loadAccountCurrentConcurrencyByIds } from '../shared/account-concurrency.js'
import { notifyAuthorizationQuotaCacheInvalidation, notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { buildSystemAccountScopeClause, canAccessAll, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, scopedSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { normalizeAccountCredentialsForWrite, requiredAccountCredentialSource } from './account-credentials-normalization.js'
import { accountCredentialFingerprint } from './account-identity.js'
import { normalizeAccountListOptions, type AccountListOptions } from './account-list-options.js'
import { cleanupDeletedAccountDetachedStats, cleanupDeletedAccountRelatedRecordData as cleanupDeletedAccountRelatedRecordDataTarget, type DeletedAccountRecordCleanupTarget } from './account-record-cleanup.js'
import { deleteAccountTagBindingsForAccounts, loadAccountTagsByAccountIds, normalizeAccountTagNamesInput, replaceAccountTags } from './account-tags.repository.js'
import { normalizeAccountModelMappingsForProvider, normalizeAccountSupportedModelsForProvider } from './account-model-normalization.js'
export { normalizeAccountModelMappingsForProvider, normalizeAccountSupportedModelsForProvider } from './account-model-normalization.js'
import { replaceAccountModelMappings } from './account-model-mappings.repository.js'
import { loadSupportedModelsByAccountIds, replaceAccountSupportedModels } from './account-supported-models.repository.js'
import {
  accountAvailabilityScheduleFromRequest,
  accountAvailabilityScheduleJson,
  isAccountAvailabilityScheduleAllowed,
  isAccountAvailabilityScheduleInputPresent,
  parseAccountAvailabilityScheduleJson
} from './account-availability-schedule.js'
import { accountCredentialsForList, findAccountRowForAccess, hydrateAccountRowsWithRuntimeState, listAccountRowsForAccess, listAccountRowsPageForAccess, loadAccountAuthorizationUsageSummaries } from './account-read.repository.js'
import { authorizationRuntimeBlockingStatus, disableExpiredAccounts } from './account-runtime-status.js'
import {
  accountGroupBinding,
  accountResourceClientCompatibility,
  accountResourceConcurrencyLimit,
  accountResourceFactAccountId,
  accountResourceProtocolCode,
  accountResourceProtocolVersion,
  accountResourceProviderCode,
  accountResourceProviderProtocolProfileId,
  accountResourceProxyProfileId,
  accountResourceType,
  findAccountSummary,
  isAuthorizedSourceAccountAvailableForDispatch,
  listAccounts,
  listAccountsPage,
  loadAuthorizationQuotaExceededByAuthorizationId,
  type AccountListResult
} from './account-summary.repository.js'
export {
  findAccountSummary,
  listAccounts,
  listAccountsPage,
  type AccountListResult
} from './account-summary.repository.js'
import {
  getAccountUsageStatsOverview as buildAccountUsageStatsOverview,
  getAccountUsageStatsOverviewPageFromWindows as buildAccountUsageStatsOverviewPageFromWindows
} from './account-usage.repository.js'
import { updateAccountUsageSnapshotRefreshState, upsertAccountUsageSnapshot } from './account-usage-snapshot.repository.js'
import { maxGroupDeleteAffectedApiKeyRoutes } from './api-key-group-binding-limits.js'
import { createApiKeyRecord, deleteApiKey, findApiKeySecret, findApiKeySummary, listApiKeys, listApiKeysPage, refreshApiKeySecret, updateApiKey } from './api-key.repository.js'
import { clearResourceAuthorizationLookupCaches, loadResourceAuthorizationSourcesByAuthorizationIds, loadResourceAuthorizationStatsByResourceIds } from './authorization-read-loaders.js'
import { decryptJson, encryptJson, maskSecret } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, getStatsDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { emptyGroupAccountStats } from './group-account-stats.mapper.js'
import { loadGroupAuthorizationUsageSummaries } from './group-read.repository.js'
import {
  findGroupSummary,
  listAccountGroupOptions,
  listGroupOptions,
  listGroups,
  listGroupsPage
} from './group-summary.repository.js'
export {
  findGroupSummary,
  listAccountGroupOptions,
  listGroupOptions,
  listGroups,
  listGroupsPage
} from './group-summary.repository.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
import { loadOpenAICodexUsageSnapshotsByAccountIds } from './oauth-usage-loaders.js'
import { defaultProviderProtocolProfile, findProviderProtocolProfile, listOpenAIProtocolProfileIds, listProviders } from './provider.repository.js'
import { resolveEnabledProxyProfileId } from './proxy.repository.js'
import { chunkValues, normalizeListPage, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { isRequestQuotaExceeded, loadRequestQuotaCostsBatch, requestQuotaCostKey, type RequestQuotaCostInput } from '../modules/gateway/quota/request-quota-checker.js'
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
import { authorizedAccountPermissions, hasActiveManualAuthorizationSource, ownerPermissions } from './resource-permissions.js'
import { findResourceAuthorizationSummary, listResourceAuthorizationSummaries, listResourceAuthorizationSummariesPage, type ResourceAuthorizationListOptions } from './resource-authorization-read.repository.js'
import {
  activeTeamMemberRows,
  assertActiveTeamGrantFanoutWithinLimit,
  cleanupInactiveAuthorizationBindings,
  deactivateAuthorizationIfNoActiveSources,
  expireDueResourceAuthorizations,
  revokeResourceAuthorizationGrant,
  returnResourceAuthorizationGrant,
  syncAccountAuthorizationInstanceNamesForSourceAccount,
  syncResourceAuthorizationGrantRuntime,
  upsertResourceAuthorizationForUser,
  upsertResourceAuthorizationGrant
} from './resource-authorization-write-state.repository.js'
import {
  invalidateAccountLookupCache,
  invalidateGroupLookupCache,
  loadSystemAccountNameMapByIds,
  loadSystemAccountPrincipalMapByIds
} from './repository-lookups.js'
import { hasEnabledRequestQuotaLimit, normalizeRequestQuotaLimits, parseRequestQuotaLimitsJson, requestQuotaLimitsJson } from './request-quota-limits.js'
import type { AccountFailureRow, AccountListRow, AccountRow, ResourceAuthorizationGrantRow, ResourceAuthorizationRow, ResourceAuthorizationSourceRow, SystemTeamRow } from './repository-row-types.js'
import { getSettings } from './settings.repository.js'
import { findSystemAccountById } from './system-accounts.repository.js'
export type { SystemTeamListOptions } from './system-team.repository.js'
export {
  addSystemTeamMembers,
  createSystemTeam,
  findSystemTeamSummary,
  listSystemTeams,
  listSystemTeamsPage,
  removeSystemTeamMember,
  updateSystemTeam
} from './system-team.repository.js'
import { markAllGroupAccountStatsDirty, markGroupAccountStatsDirty, markGroupAccountStatsDirtyByAccountIds } from './usage-stats.repository.js'
import { GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from './usage-stats-types.js'
import { emptyAccountUsageSummary, normalizeAccountUsageStatsRange, todayDateKey, usageStatsTimezone, usageSummaryFromAggregate } from './usage-stats-helpers.js'
import { loadAccountUsageSummariesForScopes, loadUsageRangeSummaryForScope, type UsageSummaryScopeRequest } from './usage-summary-loaders.js'
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
const internalAccountReadAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const deletedAccountPhysicalCleanupRetentionMonths = 1
const deletedAccountPhysicalCleanupBatchSize = 20

function findInternalAccountSummary(accountId: string): AccountSummary | undefined {
  return findAccountSummary(accountId, internalAccountReadAccess)
}

function openAIProtocolProfileIdsForQuery(): string[] {
  const profileIds = listOpenAIProtocolProfileIds().map((profileId) => profileId.trim()).filter(Boolean)
  return profileIds.length ? profileIds : [GPT_OPENAI_V1_PROFILE_ID]
}

function requireEnabledProvider(providerCode: string): ReturnType<typeof listProviders>[number] {
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${providerCode}`)
  }
  return provider
}

function requireEnabledProviderProtocolProfile(providerCode: string, profileIdInput: unknown): NonNullable<ReturnType<typeof defaultProviderProtocolProfile>> {
  const provider = requireEnabledProvider(providerCode)
  const profileId = typeof profileIdInput === 'string' && profileIdInput.trim()
    ? profileIdInput.trim()
    : provider.defaultProtocolProfileId
  const profile = profileId ? findProviderProtocolProfile(profileId) : defaultProviderProtocolProfile(providerCode)
  if (!profile || profile.providerCode !== providerCode) {
    throw new Error(`供应商协议档案无效：${profileId || providerCode}`)
  }
  if (!profile.enabled) {
    throw new Error(`供应商协议档案已停用：${profile.name}`)
  }
  return profile
}

interface OpenAIOAuthRefreshCandidateRow {
  id: string
  system_account_id: string
  provider_code: string
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
  name: string
  type: string
  status: AccountStatus
  credentials_encrypted: string
  proxy_profile_id: string | null
  concurrency_limit: number
  priority: number
  super_priority_enabled: number
  fallback_enabled: number
  client_compatibility: AccountClientCompatibility
  schedulable: number
  account_expires_at: string | null
  cooldown_until: string | null
  last_error_code: string | null
  last_error_message: string | null
  last_successful_test_model: string | null
}

export type { AccountListOptions, AccountOptionListOptions, AccountListSchedulableFilter, AccountListSortDirection, AccountListSortField } from './account-list-options.js'
export { normalizeAccountCredentialsForWrite } from './account-credentials-normalization.js'

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
export { listAccountOptions } from './account-options.repository.js'
export {
  AccountTagInUseError,
  deleteAccountTag,
  listAccountTags,
  updateAccountTags,
  type AccountTagSummary
} from './account-tags.repository.js'
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
  findApiKeySecret,
  findApiKeySummary,
  listApiKeys,
  listApiKeysPage,
  refreshApiKeySecret,
  updateApiKey
} from './api-key.repository.js'
export {
  listAuthorizationGranteeAccounts,
  listAuthorizationGranteeGroups,
  listAuthorizationGranteeTeams
} from './authorization-options.repository.js'
export { defaultProviderProtocolProfile, findProviderDefaultTestModel, findProviderProtocolProfile, isOpenAIProtocolProviderCode, listOpenAIProtocolProfileIds, listOpenAIProtocolProviderCodes, listProviders } from './provider.repository.js'
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
  revokeOtherSessionsForAccount,
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
  validateGatewayApiKey,
  type GatewayApiKeyRow
} from './gateway-api-key.repository.js'
export {
  syncApiKeyAvailabilityScheduleStatuses,
  type ApiKeyScheduleStatusSyncResult
} from './api-key-schedule-status-sync.repository.js'
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
  cleanupAuditSuccessHotRetentionAsync,
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
  listAuditLogsByIds,
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
  type AuditLogSuccessHotRetentionCleanupResult,
  type AuditOutcome,
  type AuditPayloadBlobStorageStatus,
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
  cleanupPublicApiLogsBefore,
  createPublicApiLog,
  createPublicApiLogsBatch,
  getPublicApiLogDetail,
  listPublicApiLogs,
  type PublicApiLogCaptureStatus,
  type PublicApiLogDetail,
  type PublicApiLogInput,
  type PublicApiLogListOptions,
  type PublicApiLogListResult,
  type PublicApiLogResultFilter,
  type PublicApiLogSummary
} from './public-api-logs.repository.js'
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
  runtimeOpenAIAccountCredentials,
  selectOpenAIAccountForGroup,
  type GroupUsageAccessMetadata,
  type OpenAIAccountSecret,
  type OpenAIAccountsForGroupDiagnostics,
  type OpenAIAccountsForGroupResult
} from './openai-account-selector.repository.js'
export {
  acquireBackgroundJobLease,
  createBackgroundTaskRun,
  finishBackgroundTaskRun,
  getBackgroundTaskRun,
  heartbeatBackgroundTaskRun,
  tryStartBackgroundTaskRun,
  type BackgroundTaskRunSummary
} from './background-task-runs.repository.js'

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
  listAccountQualityFailurePrecheckCandidates,
  refreshAccountQualityFromUsage,
  type AccountQualityFailurePrecheckCandidate,
  type AccountQualityRealtimeRefreshResult
} from './account-quality.repository.js'
function canUseAccount(accountId: string, systemAccountId: string): boolean {
  const row = getBusinessDatabase()
    .prepare('SELECT system_account_id, authorization_instance_authorization_id FROM accounts WHERE id = ? AND deleted_at IS NULL LIMIT 1')
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
    .prepare('SELECT authorization_instance_authorization_id FROM accounts WHERE id = ? AND system_account_id = ? AND deleted_at IS NULL LIMIT 1')
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
    .prepare('SELECT system_account_id, authorization_instance_authorization_id FROM accounts WHERE id = ? AND deleted_at IS NULL LIMIT 1')
    .get(accountId) as unknown as { system_account_id?: string; authorization_instance_authorization_id?: string | null } | undefined
  if (!row?.system_account_id) return false
  return row.system_account_id !== systemAccountId || Boolean(row.authorization_instance_authorization_id)
}

function accountRowForManage(accountId: string, access?: AccessScope): AccountRow | undefined {
  const row = getBusinessDatabase().prepare('SELECT * FROM accounts WHERE id = ? AND deleted_at IS NULL').get(accountId) as unknown as AccountRow | undefined
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
  if (account.accessType === 'authorized' && options.requireAuthorizedBinding && !account.boundGroupId) {
    return '授权账户需要先绑定到你的分组'
  }
  if (account.effectiveAvailability.available === false) {
    return account.effectiveAvailability.reason ?? account.effectiveAvailability.label
  }
  return undefined
}

export function accountTestUnavailableMessage(account: AccountSummary): string | undefined {
  if (account.accessType !== 'authorized') return undefined
  if (account.effectiveAvailability.available !== false) return undefined
  if (account.effectiveAvailability.blockerScope === 'runtime') return undefined
  if (account.effectiveAvailability.blockerScope === 'authorized_instance') {
    if (account.effectiveAvailability.status === 'instance_disabled') return undefined
    if (
      (account.effectiveAvailability.status === 'instance_error'
        || account.effectiveAvailability.status === 'instance_pending_test'
        || account.effectiveAvailability.status === 'instance_rate_limited'
        || account.effectiveAvailability.status === 'instance_temporary_unavailable'
        || account.effectiveAvailability.status === 'instance_cooldown')
      && canTestAuthorizedInstanceFailureState(account)
    ) {
      return undefined
    }
  }
  return account.effectiveAvailability.reason ?? account.effectiveAvailability.label
}

function canTestAuthorizedInstanceFailureState(account: AccountSummary): boolean {
  if (account.accessType !== 'authorized' || !account.boundGroupId) return false
  if (account.status === 'active' || account.status === 'disabled') return false
  return isAuthorizedInstanceAvailable(account)
}

const accountStatusValues: readonly AccountStatus[] = ['active', 'pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']
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
  return status === 'disabled' || status === 'pending_test' || status === 'error'
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

function validAccountIdsForGroup(providerCode: string, providerProtocolProfileId: string, accountIds: string[], systemAccountId: string): string[] {
  const uniqueIds = [...new Set(accountIds)]
  const accountsById = new Map<string, { provider_code?: string; provider_protocol_profile_id?: string }>()
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(uniqueIds, 900)) {
    const rows = database.prepare(`
      SELECT id, provider_code, provider_protocol_profile_id
      FROM accounts
      WHERE system_account_id = ?
        AND id IN (${sqlPlaceholders(chunk.length)})
    `).all(systemAccountId, ...chunk) as Array<{ id?: string; provider_code?: string; provider_protocol_profile_id?: string }>
    for (const row of rows) {
      if (row.id) {
        accountsById.set(row.id, row)
      }
    }
  }
  return uniqueIds.filter((accountId) => {
    const account = accountsById.get(accountId)
    return account?.provider_code === providerCode
      && account.provider_protocol_profile_id === providerProtocolProfileId
      && canUseAccount(accountId, systemAccountId)
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

function isDuplicateAccountNameError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return error.message.includes('idx_accounts_owner_name_unique_lower')
    || error.message.includes('accounts.system_account_id, lower(name)')
}

function assertAccountNameAvailable(systemAccountId: string, name: string, excludeId?: string): void {
  const params: string[] = [systemAccountId, name]
  const excludeClause = excludeId ? ' AND id <> ?' : ''
  if (excludeId) {
    params.push(excludeId)
  }
  const row = getBusinessDatabase()
    .prepare(`SELECT id FROM accounts WHERE system_account_id = ? AND lower(name) = lower(?) AND deleted_at IS NULL${excludeClause} LIMIT 1`)
    .get(...params) as { id?: string } | undefined
  if (row?.id) {
    throw new Error(`同一用户下账户名称已存在：${name}`)
  }
}

function isDuplicateGroupNameError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return error.message.includes('idx_groups_owner_protocol_profile_name_unique_lower')
}

function assertGroupNameAvailable(systemAccountId: string, providerProtocolProfileId: string, name: string, excludeId?: string): void {
  const params: string[] = [systemAccountId, providerProtocolProfileId, name]
  const excludeClause = excludeId ? ' AND id <> ?' : ''
  if (excludeId) {
    params.push(excludeId)
  }
  const row = getBusinessDatabase()
    .prepare(`SELECT id FROM groups WHERE system_account_id = ? AND provider_protocol_profile_id = ? AND lower(name) = lower(?)${excludeClause} LIMIT 1`)
    .get(...params) as { id?: string } | undefined
  if (row?.id) {
    throw new Error(`同一协议档案下分组名称已存在：${name}`)
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

function temporaryUnavailableRuntimeState(nowMs = Date.now()): { cooldownUntil: string; observationStartedAt: string } {
  return {
    cooldownUntil: initialTemporaryUnavailableCooldownUntil(nowMs),
    observationStartedAt: new Date(nowMs).toISOString()
  }
}

function initialCooldownUntilForStatus(status: AccountStatus, nowMs = Date.now()): string | undefined {
  if (status === 'temporary_unavailable') {
    return temporaryUnavailableRuntimeState(nowMs).cooldownUntil
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
  const row = getBusinessDatabase().prepare('SELECT * FROM accounts WHERE id = ? AND deleted_at IS NULL').get(accountId) as unknown as AccountRow | undefined
  if (!row) {
    return undefined
  }
  const resourceRow = row.authorization_instance_source_account_id
    ? getBusinessDatabase().prepare('SELECT * FROM accounts WHERE id = ? AND deleted_at IS NULL').get(row.authorization_instance_source_account_id) as unknown as AccountRow | undefined
    : undefined
  if (row.authorization_instance_authorization_id && !resourceRow) {
    return undefined
  }
  const credentialsRow = resourceRow ?? row
  return {
    ...visibleAccount,
    credentials: decryptJson<Record<string, unknown>>(credentialsRow.credentials_encrypted),
    proxyProfileId: credentialsRow.proxy_profile_id ?? undefined
  }
}

export function findAccountForCooldownRetest(accountId: string): AccountSummary | undefined {
  disableExpiredAccounts()
  return cooldownRetestDueAccountSummaries(queryAccountsDueForCooldownRetest(1, accountId))[0]
}

function findAccountCooldownRetestState(accountId: string): AccountSummary | undefined {
  disableExpiredAccounts()
  return cooldownRetestAccountSummaries(queryAccountCooldownRetestState(accountId))[0]
}

export function recordAccountSuccessfulTestModel(accountId: string, model: string, access?: AccessScope): AccountSummary | undefined {
  const normalizedModel = optionalString(model)?.trim()
  const current = findAccountSummary(accountId, access)
  if (!current?.permissions?.canUse) {
    return undefined
  }
  if (!normalizedModel) {
    return current
  }
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET last_successful_test_model = ?,
          updated_at = ?
      WHERE id = ?
        AND deleted_at IS NULL
    `)
    .run(normalizedModel, nowIso(), accountId)
  if (Number(result.changes ?? 0) > 0) {
    invalidateAccountLookupCache(accountId)
    invalidateGatewayRuntimeAfterBusinessWrite('account_test_model_updated')
  }
  return findAccountSummary(accountId, access)
}

export function listAccountsDueForCooldownRetest(limit = 20): AccountSummary[] {
  disableExpiredAccounts()
  const normalizedLimit = normalizedCooldownRetestLimit(limit)
  return cooldownRetestDueAccountSummaries(queryAccountsDueForCooldownRetest(cooldownRetestScanLimit(normalizedLimit))).slice(0, normalizedLimit)
}

function queryAccountsDueForCooldownRetest(limit: number, accountId?: string): AccountListRow[] {
  const providerProtocolProfileIds = openAIProtocolProfileIdsForQuery()
  const now = nowIso()
  const accountIdFilter = accountId ? 'AND accounts.id = ?' : ''
  const params: Array<string | number> = [
    ...providerProtocolProfileIds,
    now,
    now,
    now
  ]
  if (accountId) {
    params.push(accountId)
  }
  params.push(normalizedCooldownRetestLimit(limit))
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT ${cooldownRetestAccountSelectColumns()}
      FROM accounts
      LEFT JOIN resource_authorizations ra
        ON ra.id = accounts.authorization_instance_authorization_id
      WHERE accounts.provider_protocol_profile_id IN (${sqlPlaceholders(providerProtocolProfileIds.length)})
        AND accounts.type IN ('api_key', 'oauth')
        AND accounts.deleted_at IS NULL
        AND accounts.status IN ('temporary_unavailable', 'rate_limited')
        AND accounts.schedulable = 1
        AND accounts.cooldown_until IS NOT NULL
        AND accounts.cooldown_until <= ?
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
        AND (
          accounts.authorization_instance_authorization_id IS NULL
          OR (
            ra.id IS NOT NULL
            AND ra.status = 'active'
            AND (ra.expires_at IS NULL OR ra.expires_at > ?)
          )
        )
        ${accountIdFilter}
        AND EXISTS (
          SELECT 1
          FROM group_accounts
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = accounts.system_account_id
            AND group_accounts.enabled = 1
            AND (
              accounts.authorization_instance_authorization_id IS NULL
              OR group_accounts.account_authorization_id = accounts.authorization_instance_authorization_id
            )
        )
      ORDER BY accounts.cooldown_until ASC, accounts.priority ASC, accounts.created_at ASC, accounts.id ASC
      LIMIT ?
    `)
    .all(...params) as unknown as AccountListRow[]
  const scheduledRows = rows.filter((row) => isAccountAvailabilityScheduleAllowed(row.availability_schedule_json))
  return hydrateAccountRowsWithRuntimeState(scheduledRows, { includeCredentials: true })
    .filter((row) => row.access_type !== 'authorized' || (
      Boolean(row.source_provider_code)
      && isAuthorizedSourceAccountAvailableForDispatch(row, now)
    ))
}

function normalizedCooldownRetestLimit(limit: number): number {
  return Math.max(1, Math.min(Math.trunc(limit), 200))
}

function cooldownRetestScanLimit(limit: number): number {
  return Math.max(limit, 200)
}

function queryAccountCooldownRetestState(accountId: string): AccountListRow[] {
  const normalizedAccountId = accountId.trim()
  if (!normalizedAccountId) return []
  const providerProtocolProfileIds = openAIProtocolProfileIdsForQuery()
  return hydrateAccountRowsWithRuntimeState(getBusinessDatabase()
    .prepare(`
      SELECT ${cooldownRetestAccountSelectColumns()}
      FROM accounts
      LEFT JOIN resource_authorizations ra
        ON ra.id = accounts.authorization_instance_authorization_id
      WHERE accounts.id = ?
        AND accounts.provider_protocol_profile_id IN (${sqlPlaceholders(providerProtocolProfileIds.length)})
        AND accounts.type IN ('api_key', 'oauth')
        AND accounts.deleted_at IS NULL
      LIMIT 1
    `)
    .all(normalizedAccountId, ...providerProtocolProfileIds) as unknown as AccountListRow[], { includeCredentials: true })
}

function cooldownRetestAccountSelectColumns(): string {
  return `
        accounts.*,
        CASE WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized' ELSE 'owner' END AS access_type,
        accounts.authorization_instance_authorization_id AS authorization_id,
        ra.status AS authorization_status,
        ra.expires_at AS authorization_expires_at,
        ra.limits_json AS authorization_limits_json,
        ra.effective_source_type AS authorization_effective_source_type,
        ra.effective_source_team_id AS authorization_effective_source_team_id,
        ra.resource_owner_system_account_id AS authorization_resource_owner_system_account_id,
        ra.resource_id AS authorization_resource_id`
}

function cooldownRetestAccountSummaries(rows: AccountListRow[]): AccountSummary[] {
  if (!rows.length) return []
  const accountNames = loadSystemAccountNameMapByIds(rows.flatMap((row) => [
    row.system_account_id,
    row.authorization_resource_owner_system_account_id ?? '',
    row.authorization_instance_owner_system_account_id ?? ''
  ]))
  const currentConcurrencyByAccount = loadAccountCurrentConcurrencyByIds(rows.map((row) => row.id))
  const quotaExceededByAuthorization = loadAuthorizationQuotaExceededByAuthorizationId(rows)
  return rows.map((row) => {
    const isAuthorizedView = row.access_type === 'authorized'
    const groupBinding = accountGroupBinding(row.id, row.system_account_id)
    const displayOwnerSystemAccountId = isAuthorizedView
      ? row.authorization_resource_owner_system_account_id ?? row.authorization_instance_owner_system_account_id ?? row.system_account_id
      : row.system_account_id
    return accountSummaryWithEffectiveAvailability({
      id: row.id,
      systemAccountId: row.system_account_id,
      systemAccountName: accountNames.get(row.system_account_id),
      ownerSystemAccountId: displayOwnerSystemAccountId,
      ownerSystemAccountName: accountNames.get(displayOwnerSystemAccountId),
      providerCode: accountResourceProviderCode(row),
      providerProtocolProfileId: accountResourceProviderProtocolProfileId(row),
      protocolCode: accountResourceProtocolCode(row),
      protocolVersion: accountResourceProtocolVersion(row),
      name: row.name,
      notes: isAuthorizedView ? undefined : row.notes ?? undefined,
      type: accountResourceType(row),
      credentials: accountRuntimeCredentialsFromRow(row),
      status: row.status,
      concurrencyLimit: accountResourceConcurrencyLimit(row),
      currentConcurrency: currentConcurrencyByAccount.get(row.id) ?? 0,
      priority: row.priority,
      superPriorityEnabled: row.super_priority_enabled === 1,
      fallbackEnabled: row.fallback_enabled === 1,
      clientCompatibility: accountResourceClientCompatibility(row),
      supportedModels: row.supported_models ?? [],
      modelMappings: row.model_mappings ?? [],
      lastSuccessfulTestModel: optionalString(row.last_successful_test_model),
      proxyProfileId: accountResourceProxyProfileId(row) ?? undefined,
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
      accessType: row.access_type ?? 'owner',
      accountAuthorizationId: isAuthorizedView ? row.authorization_id ?? undefined : undefined,
      authorizationInstanceSourceAccountId: isAuthorizedView ? row.authorization_instance_source_account_id ?? undefined : undefined,
      authorizationInstanceOwnerSystemAccountId: isAuthorizedView ? row.authorization_instance_owner_system_account_id ?? row.authorization_resource_owner_system_account_id ?? undefined : undefined,
      authorizationInstanceSourceAccountStatus: isAuthorizedView ? row.source_status ?? undefined : undefined,
      authorizationInstanceSourceAccountSchedulable: isAuthorizedView && typeof row.source_schedulable === 'number' ? row.source_schedulable === 1 : undefined,
      authorizationInstanceSourceAccountAvailabilitySchedule: isAuthorizedView ? parseAccountAvailabilityScheduleJson(row.source_availability_schedule_json) : undefined,
      authorizationInstanceSourceAccountScheduleActive: isAuthorizedView ? isAccountAvailabilityScheduleAllowed(row.source_availability_schedule_json) : undefined,
      authorizationInstanceSourceAccountExpiresAt: isAuthorizedView ? row.source_account_expires_at ?? undefined : undefined,
      authorizationInstanceSourceAccountCooldownUntil: isAuthorizedView ? row.source_cooldown_until ?? undefined : undefined,
      authorizationInstanceSourceAccountLastErrorCode: isAuthorizedView ? row.source_last_error_code ?? undefined : undefined,
      authorizationInstanceSourceAccountLastErrorMessage: isAuthorizedView ? row.source_last_error_message ?? undefined : undefined,
      boundGroupId: groupBinding?.groupId,
      boundGroupName: groupBinding?.groupName,
      groupBindStatus: groupBinding?.groupBindStatus,
      bindingSystemAccountId: isAuthorizedView && groupBinding ? row.system_account_id : undefined,
      authorizationStatus: isAuthorizedView ? row.authorization_status ?? undefined : undefined,
      authorizationExpiresAt: isAuthorizedView ? row.authorization_expires_at ?? undefined : undefined,
      authorizationLimits: isAuthorizedView ? parseRequestQuotaLimitsJson(row.authorization_limits_json) : undefined,
      authorizationQuotaExceeded: isAuthorizedView && row.authorization_id ? quotaExceededByAuthorization.get(row.authorization_id) : undefined,
      permissions: isAuthorizedView ? authorizedAccountPermissions(false) : ownerPermissions()
    })
  })
}

function cooldownRetestDueAccountSummaries(rows: AccountListRow[]): AccountSummary[] {
  return cooldownRetestAccountSummaries(rows).filter((account) => {
    if (account.accessType !== 'authorized') return true
    if (!account.accountAuthorizationId || !account.bindingSystemAccountId || !account.boundGroupId || account.groupBindStatus !== 'bound') {
      return false
    }
    if (account.authorizationStatus && account.authorizationStatus !== 'active') {
      return false
    }
    if (isResourceAuthorizationExpired(account.authorizationExpiresAt)) {
      return false
    }
    if (account.authorizationQuotaExceeded) {
      return false
    }
    return true
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
      SELECT id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, credentials_encrypted,
        proxy_profile_id, concurrency_limit, priority,
        super_priority_enabled, fallback_enabled, client_compatibility, schedulable, account_expires_at, cooldown_until,
        last_error_code, last_error_message, last_successful_test_model
      FROM accounts
      WHERE authorization_instance_authorization_id IS NULL
        AND deleted_at IS NULL
        AND provider_protocol_profile_id = ?
        AND type = 'oauth'
        AND oauth_refresh_token_present = 1
        AND (status <> 'error' OR last_error_code IS NULL OR last_error_code <> ?)
        AND (oauth_access_token_expires_at IS NULL OR oauth_access_token_expires_at <= ?)
      ORDER BY oauth_access_token_expires_at IS NOT NULL ASC, oauth_access_token_expires_at ASC, updated_at ASC, id ASC
      LIMIT ?
    `)
    .all(GPT_OPENAI_V1_PROFILE_ID, input.stoppedErrorCode, dueBefore, limit) as unknown as OpenAIOAuthRefreshCandidateRow[]
  return openAIOAuthRefreshCandidateSummaries(rows)
}

export function listOpenAIOAuthStoppedRefreshExceptionAccounts(input: {
  stoppedErrorCode: string
  limit?: number
}): AccountSummary[] {
  const limit = Math.max(1, Math.min(Math.trunc(input.limit ?? 200), 500))
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, credentials_encrypted,
        proxy_profile_id, concurrency_limit, priority,
        super_priority_enabled, fallback_enabled, schedulable, account_expires_at, cooldown_until,
        last_error_code, last_error_message, last_successful_test_model
      FROM accounts
      WHERE authorization_instance_authorization_id IS NULL
        AND provider_protocol_profile_id = ?
        AND type = 'oauth'
        AND status = 'error'
        AND last_error_code = ?
      ORDER BY updated_at ASC, id ASC
      LIMIT ?
    `)
    .all(GPT_OPENAI_V1_PROFILE_ID, input.stoppedErrorCode, limit) as unknown as OpenAIOAuthRefreshCandidateRow[]
  return openAIOAuthRefreshCandidateSummaries(rows)
}

function openAIOAuthRefreshCandidateSummaries(rows: OpenAIOAuthRefreshCandidateRow[]): AccountSummary[] {
  return rows.map((row) => accountSummaryWithEffectiveAvailability({
    id: row.id,
    systemAccountId: row.system_account_id,
    providerCode: row.provider_code,
    providerProtocolProfileId: row.provider_protocol_profile_id,
    protocolCode: row.protocol_code,
    protocolVersion: row.protocol_version,
    name: row.name,
    type: 'oauth',
    credentials: decryptJson<Record<string, unknown>>(row.credentials_encrypted),
    status: row.status,
    concurrencyLimit: row.concurrency_limit,
    currentConcurrency: 0,
    priority: row.priority,
    superPriorityEnabled: row.super_priority_enabled === 1,
    fallbackEnabled: row.fallback_enabled === 1,
    clientCompatibility: normalizeOpenAIAccountClientCompatibility(
      GPT_VENDOR_CODE,
      'oauth',
      row.client_compatibility,
      'openai_standard',
      { protocolCode: row.protocol_code, protocolVersion: row.protocol_version }
    ),
    supportedModels: [],
    lastSuccessfulTestModel: optionalString(row.last_successful_test_model),
    proxyProfileId: row.proxy_profile_id ?? undefined,
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
  'providerProtocolProfileId',
  'name',
  'type',
  'credentials',
  'supportedModels',
  'modelMappings',
  'tags',
  'status',
  'concurrencyLimit',
  'priority',
  'superPriorityEnabled',
  'fallbackEnabled',
  'clientCompatibility',
  'proxyProfileId',
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
  'modelMappings',
  'tags',
  'status',
  'concurrencyLimit',
  'priority',
  'superPriorityEnabled',
  'fallbackEnabled',
  'clientCompatibility',
  'proxyProfileId',
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
  const providerProfile = requireEnabledProviderProtocolProfile(providerCode, input.providerProtocolProfileId)
  const explicitGroupId = hasOwnInput(input, 'groupId') ? normalizeNullableIdInput(input.groupId, '账户分组') : undefined
  const explicitGroup = explicitGroupId ? groupOwnerAndProvider(explicitGroupId) : undefined
  const requestedSystemAccountId = writeSystemAccountId(access)
  const systemAccountId = explicitGroup && canManageResourceOwner(explicitGroup.systemAccountId, access) ? explicitGroup.systemAccountId : requestedSystemAccountId
  const accountType = normalizedAccountType(input.type)
  if (!providerProfile.accountTypes.includes(accountType as AccountType)) {
    throw new Error(`供应商协议档案 ${providerProfile.name} 不支持账户类型 ${accountType}`)
  }
  const credentials = normalizeAccountCredentialsForWrite(accountType, input.credentials)
  const credentialSource = requiredAccountCredentialSource(accountType, credentials)
  const credentialFingerprint = typeof credentialSource === 'string' && credentialSource.trim()
    ? accountCredentialFingerprint(credentialSource)
    : null
  const oauthRefreshMetadata = openAIOAuthRefreshMetadata(accountType, credentials)
  const accountExpiresAt = hasOwnInput(input, 'accountExpiresAt')
    ? nullableServerDateTimeIso(input.accountExpiresAt, '账户套餐到期时间')
    : null
  const availabilitySchedule = accountAvailabilityScheduleFromRequest(input)
  const supportedModels = normalizeAccountSupportedModelsForProvider(input.supportedModels, providerCode, systemAccountId) ?? []
  const modelMappings = normalizeAccountModelMappingsForProvider(input.modelMappings, providerCode, systemAccountId) ?? []
  const tagNames = normalizeAccountTagNamesInput(input.tags) ?? []
  const initialStatus = normalizedAccountStatusInput(input.status, 'pending_test')
  const expiredByPackage = isAccountExpired(accountExpiresAt)
  const nextStatus = expiredByPackage ? 'disabled' : initialStatus
  const initialCooldownUntil = initialCooldownUntilForStatus(initialStatus, nowMs)
  const initialObservationStartedAt = expiredByPackage ? undefined : cooldownRetestObservationStartedAtForStatus(initialStatus, nowMs)
  const groupId = explicitGroupId
  if (!groupId) {
    throw new Error('账户分组不能为空')
  }
  const group = explicitGroupId === groupId ? explicitGroup : groupOwnerAndProvider(groupId)
  if (!group || group.systemAccountId !== systemAccountId || group.providerCode !== providerCode || group.providerProtocolProfileId !== providerProfile.id) {
    throw new Error('账户分组无效')
  }
  const proxyProfileId = globalProxyProfileId(normalizeNullableIdInput(input.proxyProfileId, '代理配置'))
  const createSuperPriorityEnabled = normalizeSuperPriorityInput(input.superPriorityEnabled, false)
  const createFallbackEnabled = normalizeFallbackInput(input.fallbackEnabled, false)
  const clientCompatibility = normalizeOpenAIAccountClientCompatibility(providerCode, accountType, input.clientCompatibility, 'openai_standard', providerProfile)
  if (nextStatus !== 'active' && (createSuperPriorityEnabled || createFallbackEnabled)) {
    throw new Error('只有正常状态的账户可以设置超级优先或降级备用')
  }
  if (createSuperPriorityEnabled && createFallbackEnabled) {
    throw new Error('超级优先和降级备用不能同时开启')
  }
  const createSchedulable = normalizeOptionalBooleanInput(input, 'schedulable', true, '账户是否参与调度')
  const account: AccountSummary = accountSummaryWithEffectiveAvailability({
    id,
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMapByIds([systemAccountId]).get(systemAccountId) : undefined,
    providerCode,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion,
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
    clientCompatibility,
    supportedModels,
    modelMappings,
    tags: tagNames.map((name) => ({ id: '', name })),
    lastSuccessfulTestModel: undefined,
    proxyProfileId,
    schedulable: expiredByPackage || nextStatus !== 'active' || isHardUnavailableAccountStatus(nextStatus) ? false : createSchedulable,
    availabilitySchedule,
    availabilityScheduleActive: isAccountAvailabilityScheduleAllowed(accountAvailabilityScheduleJson(availabilitySchedule), new Date(nowMs)),
    accountExpiresAt: accountExpiresAt ?? undefined,
    cooldownUntil: expiredByPackage ? undefined : initialCooldownUntil,
    lastErrorCode: expiredByPackage ? 'account_expired' : undefined,
    lastErrorMessage: expiredByPackage
      ? '账户套餐已过期，已自动停用'
      : initialStatus === 'pending_test'
        ? '账户创建后需测试通过才能参与调度'
        : initialCooldownUntil ? '创建时设置为临时不可调用' : undefined,
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
  })

  const database = getBusinessDatabase()
  assertAccountNameAvailable(systemAccountId, account.name)
  const transactionStarted = beginDatabaseTransaction(database)
  let savedTags = account.tags ?? []
  try {
    database
      .prepare(`
        INSERT INTO accounts (
          id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
          oauth_access_token_expires_at, oauth_refresh_token_present, proxy_profile_id, concurrency_limit,
          priority, super_priority_enabled, fallback_enabled, client_compatibility, schedulable, availability_schedule_json, notes, account_expires_at, cooldown_until, last_error_code, last_error_message,
          cooldown_retest_observation_started_at, stream_failure_count, stream_failure_window_started_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(
        account.id,
        systemAccountId,
        account.providerCode,
        providerProfile.id,
        providerProfile.protocolCode,
        providerProfile.protocolVersion,
        account.name,
        account.type,
        account.status,
        encryptJson(credentials),
        credentialFingerprint,
        maskSecret(credentialSource),
        oauthRefreshMetadata.accessTokenExpiresAt,
        oauthRefreshMetadata.refreshTokenPresent ? 1 : 0,
        account.proxyProfileId ?? null,
        account.concurrencyLimit,
        account.priority,
        account.superPriorityEnabled ? 1 : 0,
        account.fallbackEnabled ? 1 : 0,
        account.clientCompatibility,
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
    replaceAccountModelMappings(account.id, providerCode, modelMappings)
    savedTags = replaceAccountTags(account.id, systemAccountId, tagNames, now, database)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    if (isDuplicateAccountNameError(error)) {
      throw new Error(`同一用户下账户名称已存在：${account.name}`)
    }
    throw error
  }
  refreshGroupAccountStatsAfterWrite({ groupIds: [groupId], reason: 'account_created' })
  invalidateAccountLookupCache(account.id)
  invalidateGroupAccountIdsCache(groupId)
  invalidateGatewayRuntimeAfterBusinessWrite('account_created')

  return { ...account, tags: savedTags }
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
  const credentialFingerprint = typeof credentialSource === 'string' && credentialSource.trim()
    ? accountCredentialFingerprint(credentialSource)
    : null
  const oauthRefreshMetadata = openAIOAuthRefreshMetadata(current.type, credentials)
  const hasAccountExpiresAtInput = hasOwnInput(input, 'accountExpiresAt')
  const nextAccountExpiresAt = hasAccountExpiresAtInput
    ? nullableServerDateTimeIso(input.accountExpiresAt, '账户套餐到期时间')
    : current.accountExpiresAt ?? null
  const expiredByPackage = isAccountExpired(nextAccountExpiresAt)

  const hasSupportedModelsInput = hasOwnInput(input, 'supportedModels')
  const nextSupportedModels = hasSupportedModelsInput
    ? normalizeAccountSupportedModelsForProvider(input.supportedModels, current.providerCode, systemAccountId) ?? []
    : current.supportedModels ?? []
  const hasModelMappingsInput = hasOwnInput(input, 'modelMappings')
  const nextModelMappings = hasModelMappingsInput
    ? normalizeAccountModelMappingsForProvider(input.modelMappings, current.providerCode, systemAccountId) ?? []
    : current.modelMappings ?? []
  const hasTagsInput = hasOwnInput(input, 'tags')
  const nextTagNames = hasTagsInput
    ? normalizeAccountTagNamesInput(input.tags) ?? []
    : (current.tags ?? []).map((tag) => tag.name)
  const hasAvailabilityScheduleInput = isAccountAvailabilityScheduleInputPresent(input)
  const nextAvailabilitySchedule = hasAvailabilityScheduleInput
    ? accountAvailabilityScheduleFromRequest(input)
    : current.availabilitySchedule
  const hasPriorityInput = hasOwnInput(input, 'priority')
  const hasNotesInput = hasOwnInput(input, 'notes')

  const hasStatusInput = hasOwnInput(input, 'status')
  const requestedStatus = hasStatusInput ? normalizedAccountStatusInput(input.status, current.status) : current.status
  if (hasStatusInput && current.status === 'error' && requestedStatus !== 'error') {
    throw new Error('异常账户不能通过编辑切换状态，请使用恢复异常')
  }
  if (hasStatusInput && current.status === 'pending_test' && requestedStatus !== 'pending_test') {
    throw new Error('待测试账户需手动测试通过后才能参与调度')
  }
  if (hasStatusInput && requestedStatus === 'active' && (current.status === 'pending_test' || isCoolingAccountStatus(current.status) || current.status === 'error')) {
    throw new Error('待测试、临时不可调用、限流中或异常账户不能通过启用账户恢复，请使用账户测试、恢复正常或恢复异常')
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
    } else if (nextStatus === 'pending_test') {
      nextCooldownUntil = undefined
      nextLastErrorCode = undefined
      nextLastErrorMessage = '账户需测试通过后才能参与调度'
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
  const nextClientCompatibility = normalizeOpenAIAccountClientCompatibility(
    current.providerCode,
    current.type,
    hasOwnInput(input, 'clientCompatibility') ? input.clientCompatibility : current.clientCompatibility,
    current.clientCompatibility,
    current
  )

  const requestedSchedulable = normalizeOptionalBooleanInput(input, 'schedulable', current.schedulable, '账户是否参与调度')
  const updateNowMs = Date.now()
  const next: AccountSummary = accountSummaryWithEffectiveAvailability({
    ...current,
    name: normalizeOptionalRequiredTextInput(input, 'name', current.name, '账户名称'),
    notes: hasNotesInput ? normalizeNullableTextInput(input.notes, '账户备注') : current.notes,
    credentials,
    status: nextStatus,
    concurrencyLimit: normalizedPositiveIntegerInput(input.concurrencyLimit, current.concurrencyLimit, '并发限制'),
    priority: normalizedOptionalDispatchPriority(input.priority, current.priority),
    superPriorityEnabled: nextSuperPriorityEnabled,
    fallbackEnabled: nextFallbackEnabled,
    clientCompatibility: nextClientCompatibility,
    supportedModels: nextSupportedModels,
    modelMappings: nextModelMappings,
    tags: hasTagsInput ? nextTagNames.map((name) => ({ id: '', name })) : current.tags ?? [],
    proxyProfileId: hasOwnInput(input, 'proxyProfileId')
      ? globalProxyProfileId(normalizeNullableIdInput(input.proxyProfileId, '代理配置'))
      : current.proxyProfileId,
    schedulable: expiredByPackage || nextStatus !== 'active' || isHardUnavailableAccountStatus(nextStatus)
      ? false
      : hasStatusInput
        ? true
        : requestedSchedulable,
    availabilitySchedule: nextAvailabilitySchedule,
    availabilityScheduleActive: isAccountAvailabilityScheduleAllowed(accountAvailabilityScheduleJson(nextAvailabilitySchedule), new Date(updateNowMs)),
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
  })

  assertAccountNameAvailable(systemAccountId, next.name, id)
  const database = getBusinessDatabase()
  const updatedAt = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  let renamedAuthorizationInstanceIds: string[] = []
  let savedTags = next.tags ?? []
  try {
    const result = database
      .prepare(`
      UPDATE accounts
      SET name = ?, notes = ?, status = ?, credentials_encrypted = ?, credential_fingerprint = ?, credential_mask = ?,
            oauth_access_token_expires_at = ?, oauth_refresh_token_present = ?,
            proxy_profile_id = ?, concurrency_limit = ?,
            priority = ?, super_priority_enabled = ?, fallback_enabled = ?, client_compatibility = ?, schedulable = ?, availability_schedule_json = ?, account_expires_at = ?, cooldown_until = ?, last_error_code = ?, last_error_message = ?,
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
        oauthRefreshMetadata.accessTokenExpiresAt,
        oauthRefreshMetadata.refreshTokenPresent ? 1 : 0,
        next.proxyProfileId ?? null,
        next.concurrencyLimit,
        next.priority,
        next.superPriorityEnabled ? 1 : 0,
        next.fallbackEnabled ? 1 : 0,
        next.clientCompatibility,
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
      renamedAuthorizationInstanceIds = syncAccountAuthorizationInstanceNamesForSourceAccount(database, id, next.name, updatedAt)
    }
    if (Number(result.changes ?? 0) > 0 && hasSupportedModelsInput) {
      replaceAccountSupportedModels(id, next.providerCode, nextSupportedModels)
    }
    if (Number(result.changes ?? 0) > 0 && hasModelMappingsInput) {
      replaceAccountModelMappings(id, next.providerCode, nextModelMappings)
    }
    if (Number(result.changes ?? 0) > 0 && hasTagsInput) {
      savedTags = replaceAccountTags(id, systemAccountId, nextTagNames, updatedAt, database)
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
    if (isDuplicateAccountNameError(error)) {
      throw new Error(`同一用户下账户名称已存在：${next.name}`)
    }
    throw error
  }

  return { ...next, tags: savedTags }
}

export function deleteAccount(id: string, access?: AccessScope): boolean {
  return deleteAccountWithRelatedCleanup(id, access).deleted
}

export interface AccountDeleteResult {
  deleted: boolean
}

interface AccountDeleteRow {
  id: string
  system_account_id: string
  authorization_instance_authorization_id?: string | null
  authorization_instance_source_account_id?: string | null
  deleted_at?: string | null
}

interface DeletedAccountCleanupCandidateRow extends AccountDeleteRow {
  updated_at?: string | null
}

interface OrphanedAuthorizationInstanceCleanupRow extends AccountDeleteRow {
  source_deleted_at?: string | null
  resource_deleted_at?: string | null
}

interface DeletedAccountRelatedAccountRow {
  id?: string | null
  authorization_instance_authorization_id?: string | null
}

interface DeletedAccountCleanupAuthorizationRow {
  id?: string | null
  resource_id?: string | null
  grantee_system_account_id?: string | null
}

interface DeletedAccountCleanupTeamSourceRow {
  authorization_id?: string | null
  source_team_id?: string | null
}

interface ExpiredDeletedAccountBusinessCleanupTarget extends DeletedAccountRecordCleanupTarget {
  accountIds: string[]
  authorizationIds: string[]
  grantIds: string[]
}

export interface ExpiredDeletedAccountCleanupOptions {
  cutoffDeletedAt?: string
  limit?: number
}

export interface ExpiredDeletedAccountCleanupResult {
  cutoffDeletedAt: string
  orphanedAuthorizationInstances: number
  attempted: number
  completed: number
  deferred: number
  failed: number
  deletedRows: number
  physicallyDeletedAccounts: number
  physicallyDeletedAuthorizations: number
  physicallyDeletedGrants: number
  physicallyDeletedGroupBindings: number
}

export function deleteAccountWithRelatedCleanup(id: string, access?: AccessScope): AccountDeleteResult {
  const scope = buildSystemAccountScopeClause(access)
  const database = getBusinessDatabase()
  const row = database
    .prepare(`
      SELECT id, system_account_id, authorization_instance_authorization_id, authorization_instance_source_account_id, deleted_at
      FROM accounts
      WHERE id = ?
        AND deleted_at IS NULL${scope.clause}
    `)
    .get(id, ...scope.params) as unknown as AccountDeleteRow | undefined
  if (!row) {
    return { deleted: false }
  }
  if (row.authorization_instance_authorization_id) {
    throw new Error('授权账户请使用归还操作')
  }
  const actor = currentSystemAccountId(access)
  const deletedAt = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    let deletedAccountIds: string[] = []
    revokeAccountAuthorizationsForDeletedResource(database, row.id, actor, deletedAt)
    deletedAccountIds = logicallyDeleteSourceAccountWithInstances(database, row, actor, deletedAt)
    commitDatabaseTransaction(database, transactionStarted)
    if (deletedAccountIds.length > 0) {
      refreshGroupAccountStatsAfterWrite({ all: true, reason: 'account_deleted' })
      for (const accountId of deletedAccountIds) {
        invalidateAccountLookupCache(accountId)
      }
      invalidateGroupAccountIdsCache()
      clearResourceAuthorizationLookupCaches()
      invalidateGatewayRuntimeAfterBusinessWrite('account_deleted')
      invalidateAuthorizationRuntimeAfterBusinessWrite('account_deleted')
    }
    return {
      deleted: deletedAccountIds.length > 0
    }
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function logicallyDeleteSourceAccountWithInstances(database: DatabaseSync, row: AccountDeleteRow, actor: string, deletedAt: string): string[] {
  const instanceRows = database
    .prepare(`
      SELECT id
      FROM accounts
      WHERE authorization_instance_source_account_id = ?
        AND deleted_at IS NULL
      ORDER BY created_at ASC, id ASC
    `)
    .all(row.id) as unknown as Array<{ id?: string | null }>
  const accountIds = uniqueNonEmpty([row.id, ...instanceRows.map((instance) => instance.id)])
  return logicallyDeleteAccounts(database, accountIds, actor, deletedAt)
}

function logicallyDeleteAccounts(database: DatabaseSync, accountIds: string[], actor: string, deletedAt: string): string[] {
  const ids = uniqueNonEmpty(accountIds)
  if (!ids.length) return []
  const deletedIds: string[] = []
  const selectDeletedRows = database.prepare('SELECT id FROM accounts WHERE id = ? AND deleted_at = ? LIMIT 1')
  for (const chunk of chunkValues(ids, 900)) {
    database.prepare(`
      UPDATE accounts
      SET status = 'disabled',
          schedulable = 0,
          cooldown_until = NULL,
          deleted_at = ?,
          deleted_by = ?,
          updated_at = ?
      WHERE deleted_at IS NULL
        AND id IN (${sqlPlaceholders(chunk.length)})
    `).run(deletedAt, actor, deletedAt, ...chunk)
    for (const id of chunk) {
      const deletedRow = selectDeletedRows.get(id, deletedAt) as unknown as { id?: string } | undefined
      if (deletedRow?.id) {
        deletedIds.push(deletedRow.id)
      }
    }
  }
  deleteAccountTagBindingsForAccounts(deletedIds, database)
  return deletedIds
}

function uniqueNonEmpty(values: Array<string | null | undefined>): string[] {
  const output: string[] = []
  const seen = new Set<string>()
  for (const value of values) {
    const normalized = typeof value === 'string' ? value.trim() : ''
    if (!normalized || seen.has(normalized)) continue
    seen.add(normalized)
    output.push(normalized)
  }
  return output
}

function revokeAuthorizationInstanceForDeletedAccount(database: DatabaseSync, row: AccountDeleteRow, actor: string, deletedAt: string): void {
  const authorizationId = row.authorization_instance_authorization_id
  if (!authorizationId) return
  const authorization = database
    .prepare(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM resource_authorizations
      WHERE id = ?
        AND grantee_system_account_id = ?
      LIMIT 1
    `)
    .get(authorizationId, row.system_account_id) as unknown as ResourceAuthorizationRow | undefined
  if (authorization) {
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
        authorization.grantee_system_account_id
      ) as unknown as ResourceAuthorizationGrantRow[]
    for (const grant of directGrants) {
      returnResourceAuthorizationGrant(grant, actor, database, deletedAt)
    }
  }
  database
    .prepare(`
      UPDATE resource_authorization_sources
      SET status = 'revoked',
          ended_at = COALESCE(ended_at, ?),
          ended_reason = COALESCE(ended_reason, 'account_deleted'),
          revoked_by = ?,
          revoked_at = ?,
          updated_at = ?
      WHERE authorization_id = ?
        AND status IN ('active', 'superseded')
    `)
    .run(deletedAt, actor, deletedAt, deletedAt, authorizationId)
  database
    .prepare(`
      UPDATE resource_authorizations
      SET status = 'returned',
          effective_source_type = NULL,
          effective_source_team_id = NULL,
          revoked_by = ?,
          revoked_at = ?,
          revoked_reason = 'account_deleted',
          last_source_changed_at = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(actor, deletedAt, deletedAt, deletedAt, authorizationId)
  cleanupInactiveAuthorizationBindings(database, [authorizationId])
}

function revokeAccountAuthorizationsForDeletedResource(database: DatabaseSync, accountId: string, actor: string, deletedAt: string): void {
  const grants = database
    .prepare(`
      SELECT *
      FROM resource_authorization_grants
      WHERE resource_type = 'account'
        AND resource_id = ?
        AND status NOT IN ('revoked', 'returned')
      ORDER BY created_at ASC, id ASC
    `)
    .all(accountId) as unknown as ResourceAuthorizationGrantRow[]
  for (const grant of grants) {
    revokeResourceAuthorizationGrant(grant, actor, database, deletedAt)
  }
}

export function cleanupExpiredLogicallyDeletedAccounts(options: ExpiredDeletedAccountCleanupOptions = {}): ExpiredDeletedAccountCleanupResult {
  const database = getBusinessDatabase()
  const cutoffDeletedAt = options.cutoffDeletedAt?.trim() || deletedAccountPhysicalCleanupCutoffIso()
  const limit = Math.max(1, Math.min(Math.trunc(options.limit ?? deletedAccountPhysicalCleanupBatchSize), 200))
  const result: ExpiredDeletedAccountCleanupResult = {
    cutoffDeletedAt,
    orphanedAuthorizationInstances: 0,
    attempted: 0,
    completed: 0,
    deferred: 0,
    failed: 0,
    deletedRows: 0,
    physicallyDeletedAccounts: 0,
    physicallyDeletedAuthorizations: 0,
    physicallyDeletedGrants: 0,
    physicallyDeletedGroupBindings: 0
  }
  const orphanedInstanceIds = logicallyDeleteOrphanedAuthorizationInstancesForDeletedSources(database, limit)
  result.orphanedAuthorizationInstances = orphanedInstanceIds.length
  if (orphanedInstanceIds.length > 0) {
    refreshGroupAccountStatsAfterWrite({ all: true, reason: 'orphaned_authorization_instance_deleted' })
    for (const accountId of orphanedInstanceIds) {
      invalidateAccountLookupCache(accountId)
    }
    invalidateGroupAccountIdsCache()
    clearResourceAuthorizationLookupCaches()
    invalidateGatewayRuntimeAfterBusinessWrite('orphaned_authorization_instance_deleted')
    invalidateAuthorizationRuntimeAfterBusinessWrite('orphaned_authorization_instance_deleted')
  }
  const candidates = listExpiredDeletedAccountCleanupCandidates(database, cutoffDeletedAt, limit)
  for (const candidate of candidates) {
    result.attempted += 1
    try {
      const target = buildExpiredDeletedAccountBusinessCleanupTarget(database, candidate)
      const recordCleanup = cleanupDeletedAccountRelatedRecordDataTarget(target)
      result.deletedRows += recordCleanup.deletedRows
      if (recordCleanup.hasMore || recordCleanup.blockedReason) {
        result.deferred += 1
        continue
      }
      const businessCleanup = physicallyDeleteExpiredDeletedAccountBusinessRows(database, target)
      result.physicallyDeletedAccounts += businessCleanup.accounts
      result.physicallyDeletedAuthorizations += businessCleanup.authorizations
      result.physicallyDeletedGrants += businessCleanup.grants
      result.physicallyDeletedGroupBindings += businessCleanup.groupBindings
      result.completed += 1
      if (businessCleanup.accounts > 0 || businessCleanup.authorizations > 0 || businessCleanup.groupBindings > 0 || businessCleanup.grants > 0) {
        refreshGroupAccountStatsAfterWrite({ all: true, reason: 'expired_deleted_account_cleanup' })
        for (const accountId of target.accountIds) {
          invalidateAccountLookupCache(accountId)
        }
        invalidateGroupAccountIdsCache()
        clearResourceAuthorizationLookupCaches()
        invalidateGatewayRuntimeAfterBusinessWrite('expired_deleted_account_cleanup')
        invalidateAuthorizationRuntimeAfterBusinessWrite('expired_deleted_account_cleanup')
      }
    } catch {
      result.failed += 1
    }
  }
  return result
}

function logicallyDeleteOrphanedAuthorizationInstancesForDeletedSources(database: DatabaseSync, limit: number): string[] {
  const rows = database
    .prepare(`
      SELECT accounts.id, accounts.system_account_id,
        accounts.authorization_instance_authorization_id,
        accounts.authorization_instance_source_account_id,
        accounts.deleted_at,
        source_accounts.deleted_at AS source_deleted_at,
        resource_accounts.deleted_at AS resource_deleted_at
      FROM accounts
      LEFT JOIN resource_authorizations ra
        ON ra.id = accounts.authorization_instance_authorization_id
      LEFT JOIN accounts source_accounts
        ON source_accounts.id = accounts.authorization_instance_source_account_id
      LEFT JOIN accounts resource_accounts
        ON resource_accounts.id = ra.resource_id
      WHERE accounts.deleted_at IS NULL
        AND accounts.authorization_instance_authorization_id IS NOT NULL
        AND (
          ra.id IS NULL
          OR ra.resource_type <> 'account'
          OR (accounts.authorization_instance_source_account_id IS NOT NULL AND source_accounts.id IS NULL)
          OR source_accounts.deleted_at IS NOT NULL
          OR resource_accounts.id IS NULL
          OR resource_accounts.deleted_at IS NOT NULL
        )
      ORDER BY accounts.updated_at ASC, accounts.id ASC
      LIMIT ?
    `)
    .all(limit) as unknown as OrphanedAuthorizationInstanceCleanupRow[]
  if (!rows.length) return []

  const actor = internalAccountReadAccess.systemAccountId
  const fallbackDeletedAt = nowIso()
  const deletedIds: string[] = []
  for (const row of rows) {
    const deletedAt = fallbackDeletedAt
    const transactionStarted = beginDatabaseTransaction(database)
    try {
      revokeAuthorizationInstanceForDeletedSourceAccount(database, row, actor, deletedAt)
      deletedIds.push(...logicallyDeleteAccounts(database, [row.id], actor, deletedAt))
      commitDatabaseTransaction(database, transactionStarted)
    } catch (error) {
      rollbackDatabaseTransaction(database, transactionStarted)
      throw error
    }
  }
  return uniqueNonEmpty(deletedIds)
}

function revokeAuthorizationInstanceForDeletedSourceAccount(database: DatabaseSync, row: AccountDeleteRow, actor: string, deletedAt: string): void {
  const authorizationId = row.authorization_instance_authorization_id
  if (!authorizationId) return
  const authorization = database
    .prepare(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM resource_authorizations
      WHERE id = ?
      LIMIT 1
    `)
    .get(authorizationId) as unknown as ResourceAuthorizationRow | undefined
  if (authorization?.resource_type === 'account' && authorization.resource_id) {
    revokeAccountAuthorizationsForDeletedResource(database, authorization.resource_id, actor, deletedAt)
  }
  database
    .prepare(`
      UPDATE resource_authorization_sources
      SET status = 'revoked',
          ended_at = COALESCE(ended_at, ?),
          ended_reason = COALESCE(ended_reason, 'account_deleted'),
          revoked_by = ?,
          revoked_at = ?,
          updated_at = ?
      WHERE authorization_id = ?
        AND status IN ('active', 'superseded')
    `)
    .run(deletedAt, actor, deletedAt, deletedAt, authorizationId)
  database
    .prepare(`
      UPDATE resource_authorizations
      SET status = 'revoked',
          effective_source_type = NULL,
          effective_source_team_id = NULL,
          revoked_by = COALESCE(revoked_by, ?),
          revoked_at = COALESCE(revoked_at, ?),
          revoked_reason = COALESCE(revoked_reason, 'account_deleted'),
          last_source_changed_at = ?,
          updated_at = ?
      WHERE id = ?
        AND status <> 'returned'
    `)
    .run(actor, deletedAt, deletedAt, deletedAt, authorizationId)
  cleanupInactiveAuthorizationBindings(database, [authorizationId])
}

function listExpiredDeletedAccountCleanupCandidates(
  database: DatabaseSync,
  cutoffDeletedAt: string,
  limit: number
): DeletedAccountCleanupCandidateRow[] {
  const rootRows = database
    .prepare(`
      SELECT id, system_account_id, authorization_instance_authorization_id,
        authorization_instance_source_account_id, deleted_at, updated_at
      FROM accounts
      WHERE deleted_at IS NOT NULL
        AND deleted_at <= ?
        AND authorization_instance_authorization_id IS NULL
      ORDER BY deleted_at ASC, updated_at ASC, id ASC
      LIMIT ?
    `)
    .all(cutoffDeletedAt, limit) as unknown as DeletedAccountCleanupCandidateRow[]
  const remaining = limit - rootRows.length
  if (remaining <= 0) return rootRows
  const instanceRows = database
    .prepare(`
      SELECT child.id, child.system_account_id, child.authorization_instance_authorization_id,
        child.authorization_instance_source_account_id, child.deleted_at, child.updated_at
      FROM accounts child
      LEFT JOIN accounts source_accounts ON source_accounts.id = child.authorization_instance_source_account_id
      WHERE child.deleted_at IS NOT NULL
        AND child.deleted_at <= ?
        AND child.authorization_instance_authorization_id IS NOT NULL
        AND (
          child.authorization_instance_source_account_id IS NULL
          OR source_accounts.id IS NULL
          OR source_accounts.deleted_at IS NULL
          OR source_accounts.deleted_at > ?
        )
      ORDER BY child.deleted_at ASC, child.updated_at ASC, child.id ASC
      LIMIT ?
    `)
    .all(cutoffDeletedAt, cutoffDeletedAt, remaining) as unknown as DeletedAccountCleanupCandidateRow[]
  return [...rootRows, ...instanceRows]
}

function buildExpiredDeletedAccountBusinessCleanupTarget(
  database: DatabaseSync,
  row: DeletedAccountCleanupCandidateRow
): ExpiredDeletedAccountBusinessCleanupTarget {
  const isAuthorizationInstance = Boolean(row.authorization_instance_authorization_id)
  const relatedRows = isAuthorizationInstance
    ? []
    : database
      .prepare(`
        SELECT id, authorization_instance_authorization_id
        FROM accounts
        WHERE authorization_instance_source_account_id = ?
        ORDER BY created_at ASC, id ASC
      `)
      .all(row.id) as unknown as DeletedAccountRelatedAccountRow[]
  const relatedAccountIds = uniqueNonEmpty(relatedRows.map((relatedRow) => relatedRow.id))
  const accountIds = uniqueNonEmpty([row.id, ...relatedAccountIds])
  const authorizationInstanceIdsByAuthorizationId = new Map<string, string>()
  if (row.authorization_instance_authorization_id) {
    authorizationInstanceIdsByAuthorizationId.set(row.authorization_instance_authorization_id, row.id)
  }
  for (const relatedRow of relatedRows) {
    const authorizationId = typeof relatedRow.authorization_instance_authorization_id === 'string'
      ? relatedRow.authorization_instance_authorization_id.trim()
      : ''
    const accountId = typeof relatedRow.id === 'string' ? relatedRow.id.trim() : ''
    if (authorizationId && accountId) {
      authorizationInstanceIdsByAuthorizationId.set(authorizationId, accountId)
    }
  }
  const authorizationRows = loadDeletedAccountCleanupAuthorizationRows(database, accountIds, [...authorizationInstanceIdsByAuthorizationId.keys()])
  const loadedAuthorizationIds = uniqueNonEmpty(authorizationRows.map((authorizationRow) => authorizationRow.id))
  const activeAuthorizationIds = isAuthorizationInstance
    ? loadActiveDeletedAccountCleanupAuthorizationInstanceIds(database, loadedAuthorizationIds)
    : new Set<string>()
  const authorizationIds = loadedAuthorizationIds.filter((authorizationId) => !activeAuthorizationIds.has(authorizationId))
  const authorizationResourceIdById = new Map(
    authorizationRows
      .map((authorizationRow) => [String(authorizationRow.id ?? ''), String(authorizationRow.resource_id ?? '')] as const)
      .filter(([authorizationId, resourceId]) => Boolean(authorizationId && resourceId))
  )
  const teamScopeIds = loadDeletedAccountCleanupTeamScopeIds(database, authorizationIds, authorizationInstanceIdsByAuthorizationId, authorizationResourceIdById, row.id)
  const grantIds = isAuthorizationInstance
    ? loadDeletedAuthorizationInstanceGrantIds(database, authorizationIds)
    : loadDeletedSourceAccountGrantIds(database, accountIds)
  return {
    accountId: row.id,
    systemAccountId: row.system_account_id,
    relatedAccountIds,
    accountIds,
    authorizationIds,
    teamScopeIds,
    grantIds
  }
}

function loadDeletedAccountCleanupAuthorizationRows(
  database: DatabaseSync,
  accountIds: string[],
  authorizationInstanceAuthorizationIds: string[]
): DeletedAccountCleanupAuthorizationRow[] {
  const rows = new Map<string, DeletedAccountCleanupAuthorizationRow>()
  for (const chunk of chunkValues(uniqueNonEmpty(accountIds), 900)) {
    const chunkRows = database
      .prepare(`
        SELECT id, resource_id, grantee_system_account_id
        FROM resource_authorizations
        WHERE resource_type = 'account'
          AND resource_id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as unknown as DeletedAccountCleanupAuthorizationRow[]
    for (const row of chunkRows) {
      if (row.id) rows.set(row.id, row)
    }
  }
  for (const chunk of chunkValues(uniqueNonEmpty(authorizationInstanceAuthorizationIds), 900)) {
    const chunkRows = database
      .prepare(`
        SELECT id, resource_id, grantee_system_account_id
        FROM resource_authorizations
        WHERE id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as unknown as DeletedAccountCleanupAuthorizationRow[]
    for (const row of chunkRows) {
      if (row.id) rows.set(row.id, row)
    }
  }
  return [...rows.values()]
}

function loadActiveDeletedAccountCleanupAuthorizationInstanceIds(database: DatabaseSync, authorizationIds: string[]): Set<string> {
  const output = new Set<string>()
  for (const chunk of chunkValues(uniqueNonEmpty(authorizationIds), 900)) {
    const rows = database
      .prepare(`
        SELECT DISTINCT authorization_instance_authorization_id
        FROM accounts
        WHERE authorization_instance_authorization_id IN (${sqlPlaceholders(chunk.length)})
          AND deleted_at IS NULL
      `)
      .all(...chunk) as unknown as Array<{ authorization_instance_authorization_id?: string | null }>
    for (const row of rows) {
      const authorizationId = String(row.authorization_instance_authorization_id ?? '').trim()
      if (authorizationId) output.add(authorizationId)
    }
  }
  return output
}

function loadDeletedAccountCleanupTeamScopeIds(
  database: DatabaseSync,
  authorizationIds: string[],
  authorizationInstanceIdsByAuthorizationId: Map<string, string>,
  authorizationResourceIdById: Map<string, string>,
  fallbackAccountId: string
): string[] {
  const teamScopeIds: string[] = []
  for (const chunk of chunkValues(uniqueNonEmpty(authorizationIds), 900)) {
    const rows = database
      .prepare(`
        SELECT authorization_id, source_team_id
        FROM resource_authorization_sources
        WHERE authorization_id IN (${sqlPlaceholders(chunk.length)})
          AND source_team_id IS NOT NULL
      `)
      .all(...chunk) as unknown as DeletedAccountCleanupTeamSourceRow[]
    for (const row of rows) {
      const authorizationId = String(row.authorization_id ?? '').trim()
      const teamId = String(row.source_team_id ?? '').trim()
      if (!authorizationId || !teamId) continue
      const accountId = authorizationInstanceIdsByAuthorizationId.get(authorizationId)
        ?? authorizationResourceIdById.get(authorizationId)
        ?? fallbackAccountId
      teamScopeIds.push(`${accountId}:${teamId}`)
    }
  }
  return uniqueNonEmpty(teamScopeIds)
}

function loadDeletedSourceAccountGrantIds(database: DatabaseSync, accountIds: string[]): string[] {
  const grantIds: string[] = []
  for (const chunk of chunkValues(uniqueNonEmpty(accountIds), 900)) {
    grantIds.push(...(database
      .prepare(`
        SELECT id
        FROM resource_authorization_grants
        WHERE resource_type = 'account'
          AND resource_id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as unknown as Array<{ id?: string | null }>)
      .map((row) => String(row.id ?? '')))
  }
  return uniqueNonEmpty(grantIds)
}

function loadDeletedAuthorizationInstanceGrantIds(database: DatabaseSync, authorizationIds: string[]): string[] {
  const grantIds: string[] = []
  for (const chunk of chunkValues(uniqueNonEmpty(authorizationIds), 900)) {
    grantIds.push(...(database
      .prepare(`
        SELECT DISTINCT grants.id
        FROM resource_authorization_grants grants
        INNER JOIN resource_authorizations authorizations
          ON authorizations.resource_type = grants.resource_type
          AND authorizations.resource_id = grants.resource_id
          AND authorizations.resource_owner_system_account_id = grants.resource_owner_system_account_id
          AND grants.grantee_type = 'system_account'
          AND grants.grantee_system_account_id = authorizations.grantee_system_account_id
        INNER JOIN resource_authorization_sources sources
          ON sources.authorization_id = authorizations.id
          AND sources.source_type = 'manual'
        WHERE authorizations.id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as unknown as Array<{ id?: string | null }>)
      .map((row) => String(row.id ?? '')))
  }
  return uniqueNonEmpty(grantIds)
}

function physicallyDeleteExpiredDeletedAccountBusinessRows(
  database: DatabaseSync,
  target: ExpiredDeletedAccountBusinessCleanupTarget
): {
  accounts: number
  authorizations: number
  grants: number
  groupBindings: number
} {
  const accountIds = uniqueNonEmpty(target.accountIds)
  const relatedAccountIds = accountIds.filter((accountId) => accountId !== target.accountId)
  const authorizationIds = uniqueNonEmpty(target.authorizationIds)
  const grantIds = uniqueNonEmpty(target.grantIds)
  const result = {
    accounts: 0,
    authorizations: 0,
    grants: 0,
    groupBindings: 0
  }
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const chunk of chunkValues(accountIds, 900)) {
      result.groupBindings += statementChanges(database.prepare(`DELETE FROM group_accounts WHERE account_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk))
      database.prepare(`DELETE FROM account_supported_models WHERE account_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk)
      database.prepare(`DELETE FROM account_model_mappings WHERE account_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk)
      database.prepare(`DELETE FROM account_tag_bindings WHERE account_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk)
    }
    for (const chunk of chunkValues(authorizationIds, 900)) {
      result.groupBindings += statementChanges(database.prepare(`DELETE FROM group_accounts WHERE account_authorization_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk))
      database.prepare(`DELETE FROM resource_authorization_sources WHERE authorization_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk)
    }
    for (const chunk of chunkValues(grantIds, 900)) {
      result.grants += statementChanges(database.prepare(`DELETE FROM resource_authorization_grants WHERE id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk))
    }
    for (const chunk of chunkValues(relatedAccountIds, 900)) {
      result.accounts += statementChanges(database.prepare(`DELETE FROM accounts WHERE id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk))
    }
    result.accounts += statementChanges(database.prepare('DELETE FROM accounts WHERE id = ?').run(target.accountId))
    for (const chunk of chunkValues(authorizationIds, 900)) {
      result.authorizations += statementChanges(database.prepare(`DELETE FROM resource_authorizations WHERE id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk))
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  return result
}

function deletedAccountPhysicalCleanupCutoffIso(nowMs = Date.now()): string {
  const cutoff = new Date(nowMs)
  cutoff.setUTCMonth(cutoff.getUTCMonth() - deletedAccountPhysicalCleanupRetentionMonths)
  return cutoff.toISOString()
}

function statementChanges(result: { changes?: number | bigint }): number {
  return Number(result.changes ?? 0)
}

interface ClearAccountFailureStateOptions {
  allowErrorRestore?: boolean
  allowPendingTestRestore?: boolean
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
  if (current.status === 'pending_test' && options.allowPendingTestRestore !== true) {
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
          AND deleted_at IS NULL
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
        AND deleted_at IS NULL
        AND status <> 'disabled'
        AND (? = 1 OR status <> 'error')
        AND (? = 1 OR status <> 'pending_test')
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
    .run(nowIso(), id, options.allowErrorRestore === false ? 0 : 1, options.allowPendingTestRestore === true ? 1 : 0)
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
  access?: AccessScope,
  options: ClearAccountFailureStateOptions = {}
): AccountFailureStateClearResult {
  const current = findAccountSummary(id, access)
  if (!current || current.accessType !== 'authorized' || !current.boundGroupId || !current.accountAuthorizationId) {
    return { account: current, changed: false }
  }
  if (current.status === 'disabled') {
    return { account: current, changed: false }
  }
  if (current.status === 'pending_test' && options.allowPendingTestRestore !== true) {
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
  }, options)
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
        AND accounts.deleted_at IS NULL
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
          AND accounts.deleted_at IS NULL
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
    .prepare('SELECT status, updated_at, last_used_at FROM accounts WHERE id = ? AND deleted_at IS NULL LIMIT 1')
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
        AND deleted_at IS NULL
        AND status <> 'disabled'
        AND (? = 1 OR status <> 'error')
        AND (? = 1 OR status <> 'pending_test')
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
    .run(now, target.accountId, target.systemAccountId, target.accountAuthorizationId, options.allowErrorRestore === false ? 0 : 1, options.allowPendingTestRestore === true ? 1 : 0, target.systemAccountId, target.groupId, target.accountAuthorizationId)
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

export function markAuthorizedAccountBindingTemporaryUnavailableByContext(
  input: AuthorizedAccountBindingRuntimeTarget & { reason: string }
): AccountSummary | undefined {
  return markAuthorizedAccountBindingCooldownByContext({
    ...input,
    status: 'temporary_unavailable'
  })
}

export function markAuthorizedAccountBindingCooldownByContext(
  input: AuthorizedAccountBindingRuntimeTarget & { cooldownUntil?: string; reason: string; status?: AccountStatus }
): AccountSummary | undefined {
  const target = normalizedAuthorizedAccountBindingRuntimeTarget(input)
  if (!target) {
    return undefined
  }
  const cooldownStatus: AccountStatus = input.status === 'rate_limited' ? 'rate_limited' : 'temporary_unavailable'
  const cooldownNowMs = Date.now()
  const temporaryState = cooldownStatus === 'temporary_unavailable'
    ? temporaryUnavailableRuntimeState(cooldownNowMs)
    : undefined
  const cooldownUntil = cooldownStatus === 'temporary_unavailable'
    ? temporaryState!.cooldownUntil
    : input.cooldownUntil ?? initialCooldownUntilForStatus(cooldownStatus, cooldownNowMs) ?? new Date(cooldownNowMs + defaultTemporaryUnschedulableMinutes() * 60_000).toISOString()
  const observationStartedAt = temporaryState?.observationStartedAt
    ?? cooldownRetestObservationStartedAtForStatus(cooldownStatus, cooldownNowMs)
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
        AND deleted_at IS NULL
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
    .run(cooldownStatus, cooldownUntil, input.reason || null, observationStartedAt ?? null, now, target.accountId, target.systemAccountId, target.accountAuthorizationId, target.systemAccountId, target.groupId, target.accountAuthorizationId)
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
        AND deleted_at IS NULL
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
        AND deleted_at IS NULL
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
        AND deleted_at IS NULL
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
  traceId?: string
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
  const current = findAccountCooldownRetestState(id)
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
          AND deleted_at IS NULL
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
        AND deleted_at IS NULL
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
  return findAccountCooldownRetestState(id) ?? fallback
}

function normalizedCooldownRetestErrorMessage(input: CooldownAccountRetestFailureInput, errorCode: string): string {
  const message = optionalString(input.errorMessage) ?? '后台冷却复测失败'
  const parts: string[] = []
  const traceId = optionalString(input.traceId)
  if (traceId && !message.includes(traceId)) {
    parts.push(`traceId ${traceId}`)
  }
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
  const message = reason.slice(0, 1000)
  if (current.accessType === 'authorized') {
    return markAuthorizedAccountBindingTemporaryUnavailable(current, message, access)
  }
  return markAccountTemporaryUnavailable(current.id, message)
}

function markAuthorizedAccountBindingTemporaryUnavailable(
  account: AccountSummary,
  reason: string,
  access?: AccessScope
): AccountSummary | undefined {
  if (!account.boundGroupId || !account.accountAuthorizationId) {
    return undefined
  }
  const systemAccountId = authorizedBindingSystemAccountId(access)
  return markAuthorizedAccountBindingTemporaryUnavailableByContext({
    accountId: account.id,
    systemAccountId,
    groupId: account.boundGroupId,
    accountAuthorizationId: account.accountAuthorizationId,
    reason
  })
}

export function markAccountTemporaryUnavailable(id: string, reason: string): AccountSummary | undefined {
  return markAccountCooldown(id, undefined, reason, 'temporary_unavailable')
}

export function markAccountCooldown(id: string, until: string | undefined, reason: string, status: AccountStatus = 'temporary_unavailable'): AccountSummary | undefined {
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
          AND deleted_at IS NULL
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
  const temporaryState = cooldownStatus === 'temporary_unavailable'
    ? temporaryUnavailableRuntimeState(cooldownNowMs)
    : undefined
  const cooldownUntil = cooldownStatus === 'temporary_unavailable'
    ? temporaryState!.cooldownUntil
    : until ?? initialCooldownUntilForStatus(cooldownStatus, cooldownNowMs) ?? new Date(cooldownNowMs + defaultTemporaryUnschedulableMinutes() * 60_000).toISOString()
  const cooldownObservationStartedAt = temporaryState?.observationStartedAt
    ?? cooldownRetestObservationStartedAtForStatus(cooldownStatus, cooldownNowMs)

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
        AND deleted_at IS NULL
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
  const sourceTemporaryState = input.sourceStatus === 'temporary_unavailable'
    ? temporaryUnavailableRuntimeState(nowMs)
    : undefined
  const sourceCooldownUntil = sourceTemporaryState?.cooldownUntil ?? null
  const sourceObservationStartedAt = sourceTemporaryState?.observationStartedAt ?? null
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
  if ((input.clearFailureState === true || hasStatusInput) && current.status === 'pending_test') {
    throw new Error('待测试账户需手动测试通过后才能参与调度')
  }
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
          AND deleted_at IS NULL
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
  const sourceTemporaryState = input.sourceStatus === 'temporary_unavailable'
    ? temporaryUnavailableRuntimeState()
    : undefined
  const sourceCooldownUntil = sourceTemporaryState?.cooldownUntil ?? null
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
        AND deleted_at IS NULL
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
      sourceTemporaryState?.observationStartedAt ?? null,
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
        AND deleted_at IS NULL
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
  reason: string
}): { count: number; triggered: boolean; account?: AccountSummary } {
  const row = getBusinessDatabase().prepare('SELECT id, status, stream_failure_count, stream_failure_window_started_at FROM accounts WHERE id = ? AND deleted_at IS NULL').get(input.accountId) as unknown as AccountFailureRow | undefined
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
        AND deleted_at IS NULL
    `)
    .run(count, windowStartedAt, input.reason || null, nowIsoValue, input.accountId)

  const triggered = count >= Math.max(1, input.thresholdCount) && input.action !== 'none'
  if (!triggered) {
    return { count, triggered: false, account: findInternalAccountSummary(input.accountId) }
  }

  if (input.action === 'cooldown') {
    markAccountTemporaryUnavailable(input.accountId, input.reason)
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
        AND deleted_at IS NULL
    `)
    .run(nowIsoValue, input.accountId)
  refreshGroupAccountStatsAfterWrite({ accountIds: [input.accountId], reason: 'stream_failure_threshold' })

  return { count, triggered: true, account: findInternalAccountSummary(input.accountId) }
}

export function recordAuthorizedAccountBindingStreamFailure(input: AuthorizedAccountBindingRuntimeTarget & {
  thresholdCount: number
  thresholdWindowMinutes: number
  action: 'cooldown' | 'disable' | 'none'
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
    reason: input.reason
  })
  return {
    count: result.count,
    triggered: result.triggered,
    account: findAccountSummary(target.accountId, { systemAccountId: target.systemAccountId, role: 'user' }) ?? result.account
  }
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
  'providerProtocolProfileId',
  'description',
  'enabled',
  'groupType',
  'schedulingPolicy'
])

const groupUpdateInputKeys = new Set([
  'name',
  'providerCode',
  'providerProtocolProfileId',
  'description',
  'enabled',
  'groupType',
  'schedulingPolicy'
])

const authorizedGroupSettingsInputKeys = new Set([
  'enabled',
  'groupType',
  'schedulingPolicy'
])

export function createGroup(input: Record<string, unknown>, access?: AccessScope): GroupSummary {
  assertKnownInputKeys(input, groupCreateInputKeys, '分组创建参数')
  const now = nowIso()
  const systemAccountId = writeSystemAccountId(access)
  const providerCode = requiredTextInput(input.providerCode, '供应商')
  const providerProfile = requireEnabledProviderProtocolProfile(providerCode, input.providerProtocolProfileId)
  const groupType = normalizeGroupType(input.groupType)
  const schedulingPolicyJson = groupSchedulingPolicyJson(groupSchedulingPolicyInput(input), groupType)
  const name = requiredTextInput(input.name, '分组名称')
  const enabled = normalizeOptionalBooleanInput(input, 'enabled', true, '分组启用状态')
  assertGroupNameAvailable(systemAccountId, providerProfile.id, name)
  const group: GroupSummary = {
    id: newId('grp'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMapByIds([systemAccountId]).get(systemAccountId) : undefined,
    name,
    providerCode,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion,
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
      .prepare('INSERT INTO groups (id, system_account_id, name, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, description, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)')
      .run(group.id, systemAccountId, group.name, group.providerCode, providerProfile.id, providerProfile.protocolCode, providerProfile.protocolVersion, group.description ?? null, group.enabled ? 1 : 0, group.groupType, schedulingPolicyJson, now, now)
  } catch (error) {
    if (isDuplicateGroupNameError(error)) {
      throw new Error(`同一协议档案下分组名称已存在：${group.name}`)
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
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  if (current.accessType === 'authorized' && current.ownerSystemAccountId !== viewerSystemAccountId) {
    return updateAuthorizedGroupSettings(id, input, current, access)
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
  const hasProviderCodeInput = hasOwnInput(input, 'providerCode')
  const hasProviderProtocolProfileInput = hasOwnInput(input, 'providerProtocolProfileId')
  const hasGroupTypeInput = hasOwnInput(input, 'groupType')
  const hasSchedulingPolicyInput = hasGroupSchedulingPolicyInput(input)
  const nextProviderCode = hasProviderCodeInput
    ? normalizeOptionalRequiredTextInput(input, 'providerCode', current.providerCode, '供应商')
    : current.providerCode
  const nextProviderProtocolProfileId = hasProviderProtocolProfileInput
    ? normalizeOptionalRequiredTextInput(input, 'providerProtocolProfileId', current.providerProtocolProfileId ?? '', '供应商协议档案')
    : nextProviderCode === current.providerCode
      ? current.providerProtocolProfileId
      : undefined
  const nextGroupType = hasGroupTypeInput ? normalizeGroupType(input.groupType) : current.groupType
  const nextSchedulingPolicyInput = hasSchedulingPolicyInput ? groupSchedulingPolicyInput(input) : current.schedulingPolicy
  const next: GroupSummary = {
    ...current,
    name: normalizeOptionalRequiredTextInput(input, 'name', current.name, '分组名称'),
    providerCode: nextProviderCode,
    providerProtocolProfileId: nextProviderProtocolProfileId,
    description: hasDescriptionInput ? normalizeNullableTextInput(input.description, '分组说明') : current.description,
    enabled: normalizeOptionalBooleanInput(input, 'enabled', current.enabled, '分组启用状态'),
    groupType: nextGroupType,
    schedulingPolicy: parseGroupSchedulingPolicyJson(groupSchedulingPolicyJson(nextSchedulingPolicyInput, nextGroupType), nextGroupType)
  }
  if ((next.providerCode !== current.providerCode || next.providerProtocolProfileId !== current.providerProtocolProfileId) && current.accountStats.total > 0) {
    throw new Error('已有账户的分组不允许修改供应商或协议档案')
  }
  const providerProfile = requireEnabledProviderProtocolProfile(next.providerCode, next.providerProtocolProfileId)
  next.providerProtocolProfileId = providerProfile.id
  next.protocolCode = providerProfile.protocolCode
  next.protocolVersion = providerProfile.protocolVersion
  assertGroupNameAvailable(systemAccountId, providerProfile.id, next.name, id)
  const database = getBusinessDatabase()
  try {
    database
      .prepare('UPDATE groups SET name = ?, provider_code = ?, provider_protocol_profile_id = ?, protocol_code = ?, protocol_version = ?, description = ?, enabled = ?, group_type = ?, scheduling_policy_json = ?, updated_at = ? WHERE id = ? AND system_account_id = ?')
      .run(next.name, next.providerCode, next.providerProtocolProfileId, next.protocolCode, next.protocolVersion, next.description ?? null, next.enabled ? 1 : 0, next.groupType, groupSchedulingPolicyJson(nextSchedulingPolicyInput, nextGroupType), nowIso(), id, systemAccountId)
  } catch (error) {
    if (isDuplicateGroupNameError(error)) {
      throw new Error(`同一协议档案下分组名称已存在：${next.name}`)
    }
    throw error
  }
  invalidateGroupLookupCache(id)
  invalidateGatewayRuntimeAfterBusinessWrite('group_updated')
  return findGroupSummary(id, access)
}

function updateAuthorizedGroupSettings(
  id: string,
  input: Record<string, unknown>,
  current: GroupSummary,
  access?: AccessScope
): GroupSummary | undefined {
  assertKnownInputKeys(input, authorizedGroupSettingsInputKeys, '授权分组使用配置')
  const granteeSystemAccountId = userVisibleSystemAccountId(access)
  if (!granteeSystemAccountId || !current.groupAuthorizationId) {
    return undefined
  }
  const database = getBusinessDatabase()
  const authorization = database
    .prepare(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM resource_authorizations
      WHERE id = ?
        AND resource_type = 'group'
        AND resource_id = ?
        AND grantee_system_account_id = ?
        AND status IN ('active', 'paused', 'expired')
      LIMIT 1
    `)
    .get(current.groupAuthorizationId, id, granteeSystemAccountId) as unknown as ResourceAuthorizationRow | undefined
  if (!authorization || authorization.resource_owner_system_account_id === granteeSystemAccountId) {
    return undefined
  }
  const existing = database
    .prepare('SELECT enabled FROM group_authorization_settings WHERE authorization_id = ? LIMIT 1')
    .get(authorization.id) as unknown as { enabled?: number } | undefined
  const hasGroupTypeInput = hasOwnInput(input, 'groupType')
  const hasSchedulingPolicyInput = hasGroupSchedulingPolicyInput(input)
  const nextGroupType = hasGroupTypeInput ? normalizeGroupType(input.groupType) : current.groupType
  const nextSchedulingPolicyInput = hasSchedulingPolicyInput ? groupSchedulingPolicyInput(input) : current.schedulingPolicy
  const nextSchedulingPolicyJson = groupSchedulingPolicyJson(nextSchedulingPolicyInput, nextGroupType)
  const nextEnabled = normalizeOptionalBooleanInput(input, 'enabled', existing?.enabled === 0 ? false : true, '授权分组启用状态')
  const now = nowIso()
  database
    .prepare(`
      INSERT INTO group_authorization_settings (
        authorization_id, system_account_id, group_id, enabled, group_type,
        scheduling_policy_json, created_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(authorization_id) DO UPDATE SET
        system_account_id = excluded.system_account_id,
        group_id = excluded.group_id,
        enabled = excluded.enabled,
        group_type = excluded.group_type,
        scheduling_policy_json = excluded.scheduling_policy_json,
        updated_at = excluded.updated_at
    `)
    .run(
      authorization.id,
      granteeSystemAccountId,
      id,
      nextEnabled ? 1 : 0,
      nextGroupType,
      nextSchedulingPolicyJson,
      now,
      now
    )
  invalidateGatewayRuntimeAfterBusinessWrite('group_authorization_settings_updated')
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
    database.prepare('DELETE FROM api_key_group_bindings WHERE group_id = ?').run(id)
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
  systemAccountId: string
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
        api_keys.system_account_id AS systemAccountId,
        api_key_group_bindings.status AS targetBindingStatus
      FROM api_key_group_bindings
      INNER JOIN api_keys
        ON api_keys.id = api_key_group_bindings.api_key_id
        AND api_keys.system_account_id = api_key_group_bindings.system_account_id
      WHERE api_key_group_bindings.group_id = ?
      ORDER BY api_key_group_bindings.api_key_id ASC
      LIMIT ?
    `)
    .all(groupId, maxGroupDeleteAffectedApiKeyRoutes + 1) as unknown as ApiKeyAffectedByGroupDeleteRow[]
  if (!affectedApiKeys.length) return []
  if (affectedApiKeys.length > maxGroupDeleteAffectedApiKeyRoutes) {
    throw new Error(`该分组关联的 API Key 超过 ${maxGroupDeleteAffectedApiKeyRoutes} 个，请先分批解除绑定后再删除分组`)
  }

  const activeBindingCountByApiKeyId = loadActiveApiKeyGroupCountExcludingGroup(
    database,
    groupId,
    affectedApiKeys.map((apiKey) => apiKey.id)
  )
  const blockers = affectedApiKeys.filter((apiKey) => {
    if (apiKey.systemAccountId !== systemAccountId) return false
    if (apiKey.targetBindingStatus !== 'active') return false
    return (activeBindingCountByApiKeyId.get(apiKey.id) ?? 0) === 0
  })
  if (blockers.length) {
    const names = blockers.slice(0, 3).map((apiKey) => apiKey.name).join('、')
    const suffix = blockers.length > 3 ? ` 等 ${blockers.length} 个` : ''
    throw new Error(`无法删除分组：该分组仍是以下 API Key 的唯一启用号池：${names}${suffix}。请先到 API Key 管理中为这些 Key 新增并启用其他分组，或删除这些 API Key 后再删除分组。`)
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
  apiKeyIds: string[]
): Map<string, number> {
  const result = new Map<string, number>()
  const uniqueIds = [...new Set(apiKeyIds.filter(Boolean))]
  const now = nowIso()
  for (const chunk of chunkValues(uniqueIds, 500)) {
    const rows = database
      .prepare(`
        SELECT
          api_key_group_bindings.api_key_id AS apiKeyId,
          COUNT(*) AS activeBindingCount
        FROM api_key_group_bindings
        INNER JOIN groups
          ON groups.id = api_key_group_bindings.group_id
          AND groups.enabled = 1
        LEFT JOIN resource_authorizations group_authorization
          ON group_authorization.resource_type = 'group'
          AND group_authorization.resource_id = groups.id
          AND group_authorization.grantee_system_account_id = api_key_group_bindings.system_account_id
          AND group_authorization.status = 'active'
          AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
        LEFT JOIN group_authorization_settings
          ON group_authorization_settings.authorization_id = group_authorization.id
          AND group_authorization_settings.system_account_id = api_key_group_bindings.system_account_id
          AND group_authorization_settings.group_id = groups.id
        WHERE api_key_group_bindings.status = 'active'
          AND (
            groups.system_account_id = api_key_group_bindings.system_account_id
            OR (group_authorization.id IS NOT NULL AND COALESCE(group_authorization_settings.enabled, 1) = 1)
          )
          AND api_key_group_bindings.group_id <> ?
          AND api_key_group_bindings.api_key_id IN (${sqlPlaceholders(chunk.length)})
        GROUP BY api_key_group_bindings.api_key_id
    `)
      .all(now, groupId, ...chunk) as unknown as Array<{ apiKeyId: string; activeBindingCount: number }>
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
  if (!validAccountIdsForGroup(current.providerCode, current.providerProtocolProfileId ?? '', [accountId], current.systemAccountId).includes(accountId)) {
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
  const grant = findReturnableDirectGrantForGrantee(authorizationId, granteeSystemAccountId, database)
  if (!grant) return undefined
  const authorization = findRuntimeAuthorizationForDirectGrant(grant, granteeSystemAccountId, database)
  if (!authorization || authorization.resource_owner_system_account_id === granteeSystemAccountId) {
    return undefined
  }
  if (!hasActiveManualRuntimeAuthorizationSource(authorization.id, database)) {
    return undefined
  }
  const now = nowIso()
  const actor = currentSystemAccountId(access)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    returnResourceAuthorizationGrant(grant, actor, database, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshAfterResourceAuthorizationReturnedWrite()
  return database
    .prepare(`SELECT ${resourceAuthorizationSelectColumns()} FROM resource_authorizations WHERE id = ? LIMIT 1`)
    .get(authorization.id) as unknown as ResourceAuthorizationRow | undefined
}

export function returnAccountAuthorizationInstanceForGrantee(accountId: string, access?: AccessScope): ResourceAuthorizationRow | undefined {
  expireDueResourceAuthorizations()
  const granteeSystemAccountId = userVisibleSystemAccountId(access)
  if (!granteeSystemAccountId) return undefined
  const database = getBusinessDatabase()
  const row = database
    .prepare(`
      SELECT id, system_account_id, authorization_instance_authorization_id
      FROM accounts
      WHERE id = ?
        AND system_account_id = ?
        AND deleted_at IS NULL
        AND authorization_instance_authorization_id IS NOT NULL
      LIMIT 1
    `)
    .get(accountId, granteeSystemAccountId) as unknown as { id?: string; system_account_id?: string; authorization_instance_authorization_id?: string | null } | undefined
  if (!row?.authorization_instance_authorization_id) return undefined
  const authorization = database
    .prepare(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM resource_authorizations
      WHERE id = ?
        AND grantee_system_account_id = ?
      LIMIT 1
    `)
    .get(row.authorization_instance_authorization_id, granteeSystemAccountId) as unknown as ResourceAuthorizationRow | undefined
  if (!authorization || authorization.resource_owner_system_account_id === granteeSystemAccountId) {
    return undefined
  }
  if (!hasActiveManualRuntimeAuthorizationSource(authorization.id, database)) {
    return undefined
  }
  const grant = findReturnableDirectGrantForRuntimeAuthorization(authorization, granteeSystemAccountId, database)
  if (!grant) return undefined
  const now = nowIso()
  const actor = currentSystemAccountId(access)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    returnResourceAuthorizationGrant(grant, actor, database, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshAfterResourceAuthorizationReturnedWrite()
  return database
    .prepare(`SELECT ${resourceAuthorizationSelectColumns()} FROM resource_authorizations WHERE id = ? LIMIT 1`)
    .get(authorization.id) as unknown as ResourceAuthorizationRow | undefined
}

export function returnGroupAuthorizationForGrantee(groupId: string, access?: AccessScope): ResourceAuthorizationRow | undefined {
  expireDueResourceAuthorizations()
  const granteeSystemAccountId = userVisibleSystemAccountId(access)
  if (!granteeSystemAccountId) return undefined
  const database = getBusinessDatabase()
  const authorization = database
    .prepare(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM resource_authorizations
      WHERE resource_type = 'group'
        AND resource_id = ?
        AND grantee_system_account_id = ?
      LIMIT 1
    `)
    .get(groupId, granteeSystemAccountId) as unknown as ResourceAuthorizationRow | undefined
  if (!authorization || authorization.resource_owner_system_account_id === granteeSystemAccountId) {
    return undefined
  }
  if (!hasActiveManualRuntimeAuthorizationSource(authorization.id, database)) {
    return undefined
  }
  const grant = findReturnableDirectGrantForRuntimeAuthorization(authorization, granteeSystemAccountId, database)
  if (!grant) return undefined
  const now = nowIso()
  const actor = currentSystemAccountId(access)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    returnResourceAuthorizationGrant(grant, actor, database, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshAfterResourceAuthorizationReturnedWrite()
  return database
    .prepare(`SELECT ${resourceAuthorizationSelectColumns()} FROM resource_authorizations WHERE id = ? LIMIT 1`)
    .get(authorization.id) as unknown as ResourceAuthorizationRow | undefined
}

function findReturnableDirectGrantForGrantee(authorizationId: string, granteeSystemAccountId: string, database: DatabaseSync): ResourceAuthorizationGrantRow | undefined {
  return database
    .prepare(`
      SELECT *
      FROM resource_authorization_grants
      WHERE id = ?
        AND grantee_type = 'system_account'
        AND grantee_system_account_id = ?
        AND status NOT IN ('revoked', 'returned')
      LIMIT 1
    `)
    .get(authorizationId, granteeSystemAccountId) as unknown as ResourceAuthorizationGrantRow | undefined
}

function findReturnableDirectGrantForRuntimeAuthorization(authorization: ResourceAuthorizationRow, granteeSystemAccountId: string, database: DatabaseSync): ResourceAuthorizationGrantRow | undefined {
  return database
    .prepare(`
      SELECT *
      FROM resource_authorization_grants
      WHERE resource_type = ?
        AND resource_id = ?
        AND resource_owner_system_account_id = ?
        AND grantee_type = 'system_account'
        AND grantee_system_account_id = ?
        AND status NOT IN ('revoked', 'returned')
      LIMIT 1
    `)
    .get(authorization.resource_type, authorization.resource_id, authorization.resource_owner_system_account_id, granteeSystemAccountId) as unknown as ResourceAuthorizationGrantRow | undefined
}

function findRuntimeAuthorizationForDirectGrant(grant: ResourceAuthorizationGrantRow, granteeSystemAccountId: string, database: DatabaseSync): ResourceAuthorizationRow | undefined {
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

function hasActiveManualRuntimeAuthorizationSource(authorizationId: string, database: DatabaseSync): boolean {
  const row = database
    .prepare(`
      SELECT id
      FROM resource_authorization_sources
      WHERE authorization_id = ?
        AND source_type = 'manual'
        AND status = 'active'
      LIMIT 1
    `)
    .get(authorizationId) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

function refreshAfterResourceAuthorizationReturnedWrite(): void {
  refreshGroupAccountStatsAfterWrite({ all: true, reason: 'resource_authorization_returned' })
  invalidateGroupAccountIdsCache()
  clearResourceAuthorizationLookupCaches()
  invalidateAuthorizationRuntimeAfterBusinessWrite('resource_authorization_returned')
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
    .prepare('SELECT system_account_id, authorization_instance_authorization_id FROM accounts WHERE id = ? AND deleted_at IS NULL LIMIT 1')
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
    .prepare('SELECT account_expires_at FROM accounts WHERE id = ? AND deleted_at IS NULL')
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
