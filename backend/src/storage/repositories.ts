import type { AccountClientCompatibility, AccountGroupBindStatus, AccountStatus, AccountSummary, AccountTrafficMigrationSourceStatus, AccountType, AccountUsageStatsOverview, AccountUsageStatsRange, ResourceAuthorizationListResult, ResourceAuthorizationSourceStatus, ResourceAuthorizationSourceType, ResourceAuthorizationSummary } from '../domain/types.js'
import { normalizeOpenAIAccountClientCompatibility } from '../domain/account-client-compatibility.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE, isGptVendorCode } from '../domain/provider-protocol.js'
export type { GroupOptionSummary } from '../domain/types.js'
import { accountSummaryWithEffectiveAvailability } from '../domain/account-effective-availability.js'
import { loadAccountCurrentConcurrencyByIds } from '../shared/account-concurrency.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { buildSystemAccountScopeClause, canAccessAll, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { normalizeAccountCredentialsForWrite, requiredAccountCredentialSource } from './account-credentials-normalization.js'
import { accountCredentialFingerprint } from './account-identity.js'
import { normalizeAccountListOptions, type AccountListOptions } from './account-list-options.js'
import { loadAccountTagsByAccountIds, normalizeAccountTagNamesInput, replaceAccountTags } from './account-tags.repository.js'
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
import { accountCredentialsForList, findAccountRowForAccess, hydrateAccountRowsWithRuntimeState, listAccountRowsForAccess, listAccountRowsPageForAccess } from './account-read.repository.js'
import { deleteAccountWithRelatedCleanup } from './account-delete-cleanup.repository.js'
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
import { accountEnabledGroupId } from './account-group-binding-write.repository.js'
import { updateAccountUsageSnapshotRefreshState, upsertAccountUsageSnapshot } from './account-usage-snapshot.repository.js'
import { createApiKeyRecord, deleteApiKey, findApiKeySecret, findApiKeySummary, listApiKeys, listApiKeysPage, refreshApiKeySecret, updateApiKey } from './api-key.repository.js'
import { loadResourceAuthorizationSourcesByAuthorizationIds, loadResourceAuthorizationStatsByResourceIds } from './authorization-read-loaders.js'
import { decryptJson, encryptJson, maskSecret } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, getStatsDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { refreshGroupAccountStatsAfterWrite } from './group-account-stats-write-invalidation.js'
import {
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
import { listOpenAIProtocolProfileIds } from './provider.repository.js'
import { requireEnabledProviderProtocolProfile } from './provider.repository.js'
import { resolveEnabledProxyProfileId } from './proxy.repository.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { isRequestQuotaExceeded, loadRequestQuotaCostsBatch, requestQuotaCostKey, type RequestQuotaCostInput } from '../modules/gateway/quota/request-quota-checker.js'
import {
  accountSystemAccountId,
  activeAccountAuthorization,
  activeGroupAuthorization,
  activeResourceAuthorizationById,
  canManageResourceOwner,
  groupOwnerAndProvider,
  isResourceAuthorizationExpired,
  sanitizeAuthorizationSourcesForViewer,
} from './resource-authorization-helpers.js'
import { authorizedAccountPermissions, hasActiveManualAuthorizationSource, ownerPermissions } from './resource-permissions.js'
import { findResourceAuthorizationSummary, listResourceAuthorizationSummaries, listResourceAuthorizationSummariesPage, type ResourceAuthorizationListOptions } from './resource-authorization-read.repository.js'
export {
  returnAccountAuthorizationInstanceForGrantee,
  returnGroupAuthorizationForGrantee,
  returnResourceAuthorizationForGrantee
} from './resource-authorization-return.repository.js'
export {
  createResourceAuthorization,
  revokeResourceAuthorization,
  updateResourceAuthorization
} from './resource-authorization-write.repository.js'
export {
  getResourceAuthorizationUsage,
  type ResourceAuthorizationUsageOptions
} from './resource-authorization-usage.repository.js'
import {
  deactivateAuthorizationIfNoActiveSources,
  expireDueResourceAuthorizations,
  syncAccountAuthorizationInstanceNamesForSourceAccount
} from './resource-authorization-write-state.repository.js'
import {
  invalidateAccountLookupCache,
  invalidateGroupLookupCache,
  loadSystemAccountNameMapByIds,
} from './repository-lookups.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import type { AccountFailureRow, AccountListRow, AccountRow, ResourceAuthorizationSourceRow } from './repository-row-types.js'
import { getSettings } from './settings.repository.js'
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
import { emptyAccountUsageSummary, normalizeAccountUsageStatsRange, todayDateKey, usageStatsTimezone } from './usage-stats-helpers.js'
import { loadAccountUsageSummariesForScopes, type UsageSummaryScopeRequest } from './usage-summary-loaders.js'
import { loadUsageDailySeriesForScopeRequests } from './usage-window-loaders.js'
import {
  assertKnownInputKeys,
  hasOwnInput,
  normalizeNullableIdInput,
  normalizeNullableTextInput,
  normalizeOptionalBooleanInput,
  normalizeOptionalRequiredTextInput,
  requiredTextInput
} from './repository-input-normalization.js'
import {
  nullableServerDateTimeIso,
  optionalServerDateTimeIso,
  optionalString
} from './value-utils.js'

const DEFAULT_ACCOUNT_CONCURRENCY_LIMIT = 20
const manualTrafficMigrationReason = '手动迁移流量'
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

export {
  DefaultGroupReadonlyError,
  createGroup,
  deleteGroup,
  updateGroup
} from './group-write.repository.js'
export type {
  DeletedGroupApiKeyRouteChange,
  DeleteGroupResult
} from './group-write.repository.js'
export {
  addAccountToGroup,
  setAccountGroup
} from './account-group-binding-write.repository.js'

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
function accountRowForManage(accountId: string, access?: AccessScope): AccountRow | undefined {
  const row = getBusinessDatabase().prepare('SELECT * FROM accounts WHERE id = ? AND deleted_at IS NULL').get(accountId) as unknown as AccountRow | undefined
  if (!row || !canManageResourceOwner(row.system_account_id, access)) {
    return undefined
  }
  return row
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

function runDelete(sql: string, id: string): boolean {
  const result = getBusinessDatabase().prepare(sql).run(id)
  return result.changes > 0
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

function invalidateGatewayRuntimeAfterBusinessWrite(reason: string): void {
  notifyGatewayRuntimeCacheInvalidation(reason)
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

export {
  cleanupExpiredLogicallyDeletedAccounts,
  deleteAccountWithRelatedCleanup,
  type AccountDeleteResult,
  type ExpiredDeletedAccountCleanupOptions,
  type ExpiredDeletedAccountCleanupResult
} from './account-delete-cleanup.repository.js'

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

