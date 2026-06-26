import type { AccountClientCompatibility, AccountGroupBindStatus, AccountModelMapping, AccountStatus, AccountSummary, AccountSupportedEndpointMode, AccountType, AccountUsageStatsOverview, AccountUsageStatsRange, ProviderCode, ResourceAuthorizationListResult, ResourceAuthorizationSourceStatus, ResourceAuthorizationSourceType, ResourceAuthorizationSummary } from '../domain/types.js'
import { normalizeOpenAIAccountClientCompatibility } from '../domain/account-client-compatibility.js'
import { assertOpenAIEndpointModesCompatible } from '../domain/openai-endpoint-modes.js'
import { assertAnthropicEndpointModesCompatible } from '../domain/anthropic-endpoint-modes.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE, isAnthropicProtocolProfile, isGptVendorCode, isOpenAIProtocolProfile } from '../domain/provider-protocol.js'
export type { GroupOptionSummary } from '../domain/types.js'
import { accountSummaryWithEffectiveAvailability } from '../domain/account-effective-availability.js'
import { cooldownRetestObservationStartedAtForStatus, initialCooldownUntilForStatus, invalidateGatewayRuntimeAfterBusinessWrite, isAccountExpired } from './account-runtime-mutation-helpers.js'
import { buildSystemAccountScopeClause, canAccessAll, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { normalizeAccountCredentialsForWrite, requiredAccountCredentialSource } from './account-credentials-normalization.js'
import { accountCredentialFingerprint } from './account-identity.js'
import { normalizeAccountListOptions, type AccountListOptions } from './account-list-options.js'
import { maxAccountNameLength, replaceAccountNameSearchTerms, replaceAccountNameSearchTermsAsync } from './account-name-search.repository.js'
import { loadAccountTagsByAccountIds, normalizeAccountTagNamesInput, replaceAccountTags, replaceAccountTagsAsync } from './account-tags.repository.js'
import {
  assertAccountModelMappingUpstreamsAllowedBySupportedModels,
  normalizeAccountModelMappingsForProvider,
  normalizeAccountModelMappingsForProviderAsync,
  normalizeAccountSupportedModelsForProvider,
  normalizeAccountSupportedModelsForProviderAsync
} from './account-model-normalization.js'
export {
  assertAccountModelMappingUpstreamsAllowedBySupportedModels,
  normalizeAccountModelMappingsForProvider,
  normalizeAccountModelMappingsForProviderAsync,
  normalizeAccountSupportedModelsForProvider,
  normalizeAccountSupportedModelsForProviderAsync
} from './account-model-normalization.js'
import { replaceAccountModelMappings, replaceAccountModelMappingsInClientAsync } from './account-model-mappings.repository.js'
import { loadSupportedModelsByAccountIds, replaceAccountSupportedModels, replaceAccountSupportedModelsInClientAsync } from './account-supported-models.repository.js'
import {
  accountAvailabilityScheduleFromRequest,
  accountAvailabilityScheduleJson,
  isAccountAvailabilityScheduleAllowed,
  isAccountAvailabilityScheduleInputPresent,
  nextAccountAvailabilityScheduleCheckAt,
  parseAccountAvailabilityScheduleJson
} from './account-availability-schedule.js'
import { accountCredentialsForList, findAccountRowForAccess, listAccountRowsForAccess, listAccountRowsPageForAccess } from './account-read.repository.js'
import { deleteAccountWithRelatedCleanup, deleteAccountWithRelatedCleanupAsync } from './account-delete-cleanup.repository.js'
import { authorizationRuntimeBlockingStatus, disableExpiredAccounts } from './account-runtime-status.js'
import {
  isCoolingAccountStatus,
  isHardUnavailableAccountStatus,
  normalizedAccountStatusInput
} from './account-status.js'
import {
  accountCreateInputKeys,
  accountUpdateInputKeys,
  normalizedAccountType,
  normalizedOptionalDispatchPriority,
  normalizedPositiveIntegerInput,
  normalizeFallbackInput,
  normalizeSuperPriorityInput,
  openAIOAuthRefreshMetadata
} from './account-write-input.js'
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
  findAccountSummaryAsync,
  listAccounts,
  listAccountsPageAsync,
  listAccountsPage,
  type AccountListResult
} from './account-summary.repository.js'
export {
  findAccountSummary,
  findAccountSummaryAsync,
  listAccounts,
  listAccountsPageAsync,
  listAccountsPage,
  type AccountListResult
} from './account-summary.repository.js'
import {
  getAccountUsageStatsOverview as buildAccountUsageStatsOverview,
  getAccountUsageStatsOverviewPageFromWindows as buildAccountUsageStatsOverviewPageFromWindows
} from './account-usage.repository.js'
import { accountEnabledGroupId } from './account-group-binding-write.repository.js'
import { updateAccountUsageSnapshotRefreshState, upsertAccountUsageSnapshot } from './account-usage-snapshot.repository.js'
import { createApiKeyRecord, createApiKeyRecordAsync, deleteApiKey, deleteApiKeyAsync, findApiKeySecret, findApiKeySecretAsync, findApiKeySummary, findApiKeySummaryAsync, listApiKeys, listApiKeysAsync, listApiKeysPage, listApiKeysPageAsync, refreshApiKeySecret, refreshApiKeySecretAsync, updateApiKey, updateApiKeyAsync } from './api-key.repository.js'
import { loadResourceAuthorizationSourcesByAuthorizationIds, loadResourceAuthorizationStatsByResourceIds } from './authorization-read-loaders.js'
import { decryptJson, encryptJson, maskSecret } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, getStatsDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { runtimeConfig } from '../config/runtime.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { refreshGroupAccountStatsAfterWrite } from './group-account-stats-write-invalidation.js'
import {
  listAccountGroupOptions,
  listAccountGroupOptionsAsync,
  listGroupOptions,
  listGroupOptionsAsync,
  listGroups,
  listGroupsAsync,
  listGroupsPage,
  listGroupsPageAsync
} from './group-summary.repository.js'
export {
  findGroupSummary,
  findGroupSummaryAsync,
  listAccountGroupOptions,
  listAccountGroupOptionsAsync,
  listGroupOptions,
  listGroupOptionsAsync,
  listGroups,
  listGroupsAsync,
  listGroupsPage,
  listGroupsPageAsync
} from './group-summary.repository.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
import { loadOpenAICodexUsageSnapshotsByAccountIds } from './oauth-usage-loaders.js'
import { listOpenAIProtocolProfileIds } from './provider.repository.js'
import { requireEnabledProviderProtocolProfile, requireEnabledProviderProtocolProfileAsync } from './provider.repository.js'
import { getPostgresPool } from './postgres-client.js'
import { ProxyProfileUnavailableError, resolveEnabledProxyProfileId } from './proxy.repository.js'
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
  sanitizeAuthorizationSourcesForViewer
} from './resource-authorization-helpers.js'
import { authorizedAccountPermissions, hasActiveManualAuthorizationSource, ownerPermissions } from './resource-permissions.js'
import { findResourceAuthorizationSummary, listResourceAuthorizationSummaries, listResourceAuthorizationSummariesPage, type ResourceAuthorizationListOptions } from './resource-authorization-read.repository.js'
export {
  returnAccountAuthorizationInstanceForGrantee,
  returnAccountAuthorizationInstanceForGranteeAsync,
  returnGroupAuthorizationForGrantee,
  returnGroupAuthorizationForGranteeAsync,
  returnResourceAuthorizationForGrantee,
  returnResourceAuthorizationForGranteeAsync
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
  loadSystemAccountNameMapByIds
} from './repository-lookups.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import type { AccountRow, ResourceAuthorizationSourceRow } from './repository-row-types.js'
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
export {
  findAccountForCooldownRetest,
  listAccountsDueForCooldownRetest,
  recordCooldownAccountRetestFailure,
  type CooldownAccountRetestFailureInput,
  type CooldownAccountRetestFailureResult
} from './account-cooldown-retest.repository.js'
export {
  findAccountForHealthCheck,
  listAccountsDueForHealthCheck,
  normalizedHealthCheckSettings,
  recordAccountHealthCheckFailure,
  recordAccountHealthCheckSuccess,
  recordAccountHealthSuccessSignals,
  type AccountHealthCheckFailureResult,
  type AccountHealthCheckListOptions,
  type AccountHealthCheckSettings
} from './account-health-check.repository.js'
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
  requiredTextInput
} from './repository-input-normalization.js'
import {
  nullableServerDateTimeIso,
  optionalString
} from './value-utils.js'

const DEFAULT_ACCOUNT_CONCURRENCY_LIMIT = 20
const internalAccountReadAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const deletedAccountPhysicalCleanupRetentionMonths = 1
const deletedAccountPhysicalCleanupBatchSize = 20

function normalizeAccountNameInput(value: unknown): string {
  const name = requiredTextInput(value, '账户名称')
  assertAccountNameWithinLimit(name)
  return name
}

function normalizeOptionalAccountNameInput(input: Record<string, unknown>, fallback: string): string {
  if (!hasOwnInput(input, 'name')) return fallback
  return normalizeAccountNameInput(input.name)
}

function assertAccountNameWithinLimit(name: string): void {
  if ([...name].length > maxAccountNameLength) {
    throw new Error(`账户名称不能超过 ${maxAccountNameLength} 个字符`)
  }
}

function assertAccountEndpointModesCompatible(
  protocolProfile: {
    id?: string
    providerCode?: string
    providerProtocolProfileId?: string
    protocolCode?: string
    protocolVersion?: string
  },
  input: {
    modes: readonly AccountSupportedEndpointMode[]
    modelMappings?: readonly AccountModelMapping[]
    accountType?: string
    clientCompatibility: AccountClientCompatibility
  }
): void {
  if (isAnthropicProtocolProfile(protocolProfile)) {
    assertAnthropicEndpointModesCompatible({
      modes: input.modes,
      accountType: input.accountType
    })
    return
  }
  if (isOpenAIProtocolProfile(protocolProfile)) {
    assertOpenAIEndpointModesCompatible({
      modes: input.modes,
      modelMappings: input.modelMappings,
      providerCode: protocolProfile.providerCode,
      providerProtocolProfileId: protocolProfile.providerProtocolProfileId ?? protocolProfile.id,
      accountType: input.accountType,
      clientCompatibility: input.clientCompatibility
    })
  }
}

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
  createGroupAsync,
  deleteGroup,
  deleteGroupAsync,
  updateGroup,
  updateGroupAsync
} from './group-write.repository.js'
export type {
  DeletedGroupApiKeyRouteChange,
  DeleteGroupResult
} from './group-write.repository.js'
export {
  addAccountToGroup,
  setAccountGroup,
  setAccountGroupAsync
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
export { listAccountOptions, listAccountOptionsAsync } from './account-options.repository.js'
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
  createApiKeyRecordAsync,
  deleteApiKey,
  deleteApiKeyAsync,
  deleteApiKeyWithRelatedCleanup,
  deleteApiKeyWithRelatedCleanupAsync,
  findApiKeySecret,
  findApiKeySecretAsync,
  findApiKeySummary,
  findApiKeySummaryAsync,
  listApiKeys,
  listApiKeysAsync,
  listApiKeysPage,
  listApiKeysPageAsync,
  refreshApiKeySecret,
  refreshApiKeySecretAsync,
  updateApiKey,
  updateApiKeyAsync
} from './api-key.repository.js'
export {
  listAuthorizationGranteeAccounts,
  listAuthorizationGranteeGroups,
  listAuthorizationGranteeTeams
} from './authorization-options.repository.js'
export {
  defaultProviderProtocolProfile,
  defaultProviderProtocolProfileAsync,
  findProviderDefaultTestModel,
  findProviderDefaultTestModelAsync,
  findProviderProtocolProfile,
  findProviderProtocolProfileAsync,
  isOpenAIProtocolProviderCode,
  isOpenAIProtocolProviderCodeAsync,
  isProtocolProviderCodeAsync,
  listAnthropicProtocolProviderCodesAsync,
  listGeminiProtocolProviderCodes,
  listGeminiProtocolProviderCodesAsync,
  listOpenAIProtocolProfileIds,
  listOpenAIProtocolProfileIdsAsync,
  listOpenAIProtocolProviderCodes,
  listOpenAIProtocolProviderCodesAsync,
  listProviders,
  listProvidersAsync,
  requireEnabledProviderProtocolProfileAsync
} from './provider.repository.js'
export {
  createSession,
  createSessionAsync,
  createSystemAccount,
  createSystemAccountAsync,
  createSystemAccountWithPasswordHash,
  createSystemAccountWithPasswordHashAsync,
  findSessionByToken,
  findSessionByTokenAsync,
  findSystemAccountById,
  findSystemAccountByIdAsync,
  findSystemAccountByUsername,
  findSystemAccountByUsernameAsync,
  listSystemAccountOptions,
  listSystemAccountOptionsAsync,
  listSystemAccounts,
  listSystemAccountsAsync,
  listSystemAccountsPage,
  listSystemAccountsPageAsync,
  revokeAllSessionsForAccount,
  revokeAllSessionsForAccountAsync,
  revokeOtherSessionsForAccount,
  revokeOtherSessionsForAccountAsync,
  revokeSession,
  revokeSessionAsync,
  touchSession,
  touchSessionAsync,
  updateSystemAccount,
  updateSystemAccountAsync,
  updateSystemAccountLastLogin,
  updateSystemAccountLastLoginAsync,
  updateSystemAccountWithPasswordHash,
  updateSystemAccountWithPasswordHashAsync,
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
  getSettingsAsync,
  listPublicGlobalSettings,
  listPublicGlobalSettingsAsync,
  listGlobalSettings,
  listGlobalSettingsAsync,
  updateGlobalSettings,
  updateGlobalSettingsAsync,
  updateSettings,
  updateSettingsAsync
} from './settings.repository.js'
export {
  clearGatewayApiKeyValidationCache,
  findActiveGatewayApiKeyById,
  validateGatewayApiKey,
  validateGatewayApiKeyAsync,
  type GatewayApiKeyRow
} from './gateway-api-key.repository.js'
export {
  syncApiKeyAvailabilityScheduleStatuses,
  type ApiKeyScheduleStatusSyncResult
} from './api-key-schedule-status-sync.repository.js'
export {
  syncAccountAvailabilityScheduleStatuses,
  type AccountAvailabilityScheduleStatusSyncResult
} from './account-availability-schedule-status-sync.repository.js'
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
  createUsageRecordsBatchAsync,
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
  listOpenAIAccountsForGroup,
  listOpenAIAccountsForGroupResult,
  listOpenAIAccountsForGroupResultAsync,
  listRecoverableUnavailableOpenAIAccountsForGroup,
  resolveGroupUsageAccessMetadata,
  resolveGroupUsageAccessMetadataAsync,
  runtimeOpenAIAccountCredentials,
  selectOpenAIAccountForGroup,
  type DispatchAccountSecret,
  type DispatchAccountsForGroupResult,
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

function optionalNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
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

async function assertAccountNameAvailableAsync(client: DatabaseClient, systemAccountId: string, name: string, excludeId?: string): Promise<void> {
  const params: string[] = [systemAccountId, name]
  const excludeClause = excludeId ? ' AND id <> ?' : ''
  if (excludeId) {
    params.push(excludeId)
  }
  const row = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${accountWriteTable(client, 'accounts')}
    WHERE system_account_id = ? AND lower(name) = lower(?) AND deleted_at IS NULL${excludeClause}
    LIMIT 1
  `, params)
  if (row?.id) {
    throw new Error(`同一用户下账户名称已存在：${name}`)
  }
}

async function groupOwnerAndProviderForAccountWriteAsync(client: DatabaseClient, groupId: string): Promise<{ systemAccountId: string; providerCode: ProviderCode; providerProtocolProfileId: string; protocolCode: string; protocolVersion: string; name?: string } | undefined> {
  const row = await client.one<{ system_account_id?: string; provider_code?: ProviderCode; provider_protocol_profile_id?: string; protocol_code?: string; protocol_version?: string; name?: string }>(`
    SELECT system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name
    FROM ${accountWriteTable(client, 'groups')}
    WHERE id = ?
  `, [groupId])
  return row?.system_account_id && row.provider_code && row.provider_protocol_profile_id && row.protocol_code && row.protocol_version
    ? {
        systemAccountId: row.system_account_id,
        providerCode: row.provider_code,
        providerProtocolProfileId: row.provider_protocol_profile_id,
        protocolCode: row.protocol_code,
        protocolVersion: row.protocol_version,
        name: row.name
      }
    : undefined
}

async function resolveEnabledProxyProfileIdForAccountWriteAsync(client: DatabaseClient, proxyProfileId?: string): Promise<string | undefined> {
  if (!proxyProfileId) return undefined
  const row = await client.one<{ id?: string; enabled?: number }>(`
    SELECT id, enabled
    FROM ${accountWriteTable(client, 'proxy_profiles')}
    WHERE id = ?
  `, [proxyProfileId])
  if (!row?.id || row.enabled !== 1) {
    throw new ProxyProfileUnavailableError(proxyProfileId)
  }
  return row.id
}

async function loadSystemAccountNameForAccountWriteAsync(client: DatabaseClient, systemAccountId: string): Promise<string | undefined> {
  const row = await client.one<{ display_name?: string }>(`
    SELECT display_name
    FROM ${accountWriteTable(client, 'system_accounts')}
    WHERE id = ?
  `, [systemAccountId])
  return row?.display_name
}

function accountWriteTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
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

export async function findAccountForTestAsync(accountId: string, access?: AccessScope): Promise<AccountSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findAccountForTest(accountId, access)
  }
  const accountAccess = access ?? internalAccountReadAccess
  const visibleAccount = await findAccountSummaryAsync(accountId, accountAccess)
  if (!visibleAccount?.permissions?.canUse) {
    return undefined
  }
  return visibleAccount
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
  const clientCompatibility = normalizeOpenAIAccountClientCompatibility(providerCode, accountType, input.clientCompatibility, 'openai_standard', providerProfile)
  const credentials = normalizeAccountCredentialsForWrite(accountType, input.credentials, {
    providerCode,
    accountType,
    clientCompatibility,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion
  })
  const credentialSource = requiredAccountCredentialSource(accountType, credentials)
  const credentialFingerprint = typeof credentialSource === 'string' && credentialSource.trim()
    ? accountCredentialFingerprint(credentialSource)
    : null
  const oauthRefreshMetadata = openAIOAuthRefreshMetadata(accountType, credentials)
  const accountExpiresAt = hasOwnInput(input, 'accountExpiresAt')
    ? nullableServerDateTimeIso(input.accountExpiresAt, '账户套餐到期时间')
    : null
  const availabilitySchedule = accountAvailabilityScheduleFromRequest(input)
  const hasAvailabilityScheduleActiveInput = hasOwnInput(input, 'availabilityScheduleActive')
  const supportedModels = normalizeAccountSupportedModelsForProvider(input.supportedModels, providerCode, systemAccountId) ?? []
  const modelMappings = normalizeAccountModelMappingsForProvider(input.modelMappings, providerCode, systemAccountId, providerProfile, {
    supportedEndpointModes: credentials.supported_endpoint_modes as AccountSupportedEndpointMode[]
  }) ?? []
  assertAccountModelMappingUpstreamsAllowedBySupportedModels(modelMappings, supportedModels)
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
  assertAccountEndpointModesCompatible(providerProfile, {
    modes: credentials.supported_endpoint_modes as AccountSupportedEndpointMode[],
    modelMappings,
    accountType,
    clientCompatibility
  })
  if (nextStatus !== 'active' && (createSuperPriorityEnabled || createFallbackEnabled)) {
    throw new Error('只有正常状态的账户可以设置超级优先或降级备用')
  }
  if (createSuperPriorityEnabled && createFallbackEnabled) {
    throw new Error('超级优先和降级备用不能同时开启')
  }
  const createSchedulable = normalizeOptionalBooleanInput(input, 'schedulable', true, '账户是否参与调度')
  const availabilityScheduleActive = hasAvailabilityScheduleActiveInput
    ? normalizeAccountAvailabilityScheduleActiveOverride(input.availabilityScheduleActive, availabilitySchedule)
    : isAccountAvailabilityScheduleAllowed(accountAvailabilityScheduleJson(availabilitySchedule), new Date(nowMs))
  const account: AccountSummary = accountSummaryWithEffectiveAvailability({
    id,
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMapByIds([systemAccountId]).get(systemAccountId) : undefined,
    providerCode,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion,
    name: normalizeAccountNameInput(input.name),
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
    availabilityScheduleActive,
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
  const availabilityScheduleNextCheckAt = nextAccountAvailabilityScheduleCheckAt(account.availabilitySchedule, new Date(nowMs))
  let savedTags = account.tags ?? []
  try {
    database
      .prepare(`
        INSERT INTO accounts (
          id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
          oauth_access_token_expires_at, oauth_refresh_token_present, proxy_profile_id, concurrency_limit,
          priority, super_priority_enabled, fallback_enabled, client_compatibility, schedulable, availability_schedule_json, availability_schedule_active, availability_schedule_next_check_at, notes, account_expires_at, cooldown_until, last_error_code, last_error_message,
          cooldown_retest_observation_started_at, stream_failure_count, stream_failure_window_started_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
        account.availabilityScheduleActive ? 1 : 0,
        availabilityScheduleNextCheckAt,
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
    replaceAccountNameSearchTerms(database, account.id, systemAccountId, account.name, now)
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

export async function createAccountAsync(input: Record<string, unknown>, access?: AccessScope): Promise<AccountSummary> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createAccount(input, access)
  }
  assertKnownInputKeys(input, accountCreateInputKeys, '账户创建参数')
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const nowMs = Date.now()
  const now = new Date(nowMs).toISOString()
  const id = newId('acc')
  const providerCode = requiredTextInput(input.providerCode, '供应商')
  const providerProfile = await requireEnabledProviderProtocolProfileAsync(providerCode, input.providerProtocolProfileId)
  const explicitGroupId = hasOwnInput(input, 'groupId') ? normalizeNullableIdInput(input.groupId, '账户分组') : undefined
  const explicitGroup = explicitGroupId ? await groupOwnerAndProviderForAccountWriteAsync(client, explicitGroupId) : undefined
  const requestedSystemAccountId = writeSystemAccountId(access)
  const systemAccountId = explicitGroup && canManageResourceOwner(explicitGroup.systemAccountId, access) ? explicitGroup.systemAccountId : requestedSystemAccountId
  const accountType = normalizedAccountType(input.type)
  if (!providerProfile.accountTypes.includes(accountType as AccountType)) {
    throw new Error(`供应商协议档案 ${providerProfile.name} 不支持账户类型 ${accountType}`)
  }
  const clientCompatibility = normalizeOpenAIAccountClientCompatibility(providerCode, accountType, input.clientCompatibility, 'openai_standard', providerProfile)
  const credentials = normalizeAccountCredentialsForWrite(accountType, input.credentials, {
    providerCode,
    accountType,
    clientCompatibility,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion
  })
  const credentialSource = requiredAccountCredentialSource(accountType, credentials)
  const credentialFingerprint = typeof credentialSource === 'string' && credentialSource.trim()
    ? accountCredentialFingerprint(credentialSource)
    : null
  const oauthRefreshMetadata = openAIOAuthRefreshMetadata(accountType, credentials)
  const accountExpiresAt = hasOwnInput(input, 'accountExpiresAt')
    ? nullableServerDateTimeIso(input.accountExpiresAt, '账户套餐到期时间')
    : null
  const availabilitySchedule = accountAvailabilityScheduleFromRequest(input)
  const hasAvailabilityScheduleActiveInput = hasOwnInput(input, 'availabilityScheduleActive')
  const supportedModels = await normalizeAccountSupportedModelsForProviderAsync(input.supportedModels, providerCode, systemAccountId) ?? []
  const modelMappings = await normalizeAccountModelMappingsForProviderAsync(input.modelMappings, providerCode, systemAccountId, providerProfile, {
    supportedEndpointModes: credentials.supported_endpoint_modes as AccountSupportedEndpointMode[]
  }) ?? []
  assertAccountModelMappingUpstreamsAllowedBySupportedModels(modelMappings, supportedModels)
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
  const group = explicitGroupId === groupId ? explicitGroup : await groupOwnerAndProviderForAccountWriteAsync(client, groupId)
  if (!group || group.systemAccountId !== systemAccountId || group.providerCode !== providerCode || group.providerProtocolProfileId !== providerProfile.id) {
    throw new Error('账户分组无效')
  }
  const requestedProxyProfileId = normalizeNullableIdInput(input.proxyProfileId, '代理配置')
  const proxyProfileId = await resolveEnabledProxyProfileIdForAccountWriteAsync(client, requestedProxyProfileId)
  const createSuperPriorityEnabled = normalizeSuperPriorityInput(input.superPriorityEnabled, false)
  const createFallbackEnabled = normalizeFallbackInput(input.fallbackEnabled, false)
  assertAccountEndpointModesCompatible(providerProfile, {
    modes: credentials.supported_endpoint_modes as AccountSupportedEndpointMode[],
    modelMappings,
    accountType,
    clientCompatibility
  })
  if (nextStatus !== 'active' && (createSuperPriorityEnabled || createFallbackEnabled)) {
    throw new Error('只有正常状态的账户可以设置超级优先或降级备用')
  }
  if (createSuperPriorityEnabled && createFallbackEnabled) {
    throw new Error('超级优先和降级备用不能同时开启')
  }
  const createSchedulable = normalizeOptionalBooleanInput(input, 'schedulable', true, '账户是否参与调度')
  const availabilityScheduleActive = hasAvailabilityScheduleActiveInput
    ? normalizeAccountAvailabilityScheduleActiveOverride(input.availabilityScheduleActive, availabilitySchedule)
    : isAccountAvailabilityScheduleAllowed(accountAvailabilityScheduleJson(availabilitySchedule), new Date(nowMs))
  const systemAccountName = includeSystemAccountFields(access)
    ? await loadSystemAccountNameForAccountWriteAsync(client, systemAccountId)
    : undefined
  const account: AccountSummary = accountSummaryWithEffectiveAvailability({
    id,
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName,
    providerCode,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion,
    name: normalizeAccountNameInput(input.name),
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
    availabilityScheduleActive,
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

  await assertAccountNameAvailableAsync(client, systemAccountId, account.name)
  const availabilityScheduleNextCheckAt = nextAccountAvailabilityScheduleCheckAt(account.availabilitySchedule, new Date(nowMs))
  let savedTags = account.tags ?? []
  try {
    await client.transaction(async (tx) => {
      await tx.execute(`
        INSERT INTO ${accountWriteTable(tx, 'accounts')} (
          id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
          oauth_access_token_expires_at, oauth_refresh_token_present, proxy_profile_id, concurrency_limit,
          priority, super_priority_enabled, fallback_enabled, client_compatibility, schedulable, availability_schedule_json, availability_schedule_active, availability_schedule_next_check_at, notes, account_expires_at, cooldown_until, last_error_code, last_error_message,
          cooldown_retest_observation_started_at, stream_failure_count, stream_failure_window_started_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `, [
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
        account.availabilityScheduleActive ? 1 : 0,
        availabilityScheduleNextCheckAt,
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
      ])
      await tx.execute(`
        INSERT INTO ${accountWriteTable(tx, 'group_accounts')} (
          system_account_id, group_id, account_id,
          local_priority, local_super_priority_enabled, local_fallback_enabled,
          enabled, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
      `, [
        systemAccountId,
        groupId,
        account.id,
        account.priority,
        account.superPriorityEnabled ? 1 : 0,
        account.fallbackEnabled ? 1 : 0,
        now,
        now
      ])
      await replaceAccountSupportedModelsInClientAsync(tx, account.id, providerCode, supportedModels)
      await replaceAccountModelMappingsInClientAsync(tx, account.id, providerCode, modelMappings)
      await replaceAccountNameSearchTermsAsync(tx, account.id, systemAccountId, account.name, now)
      savedTags = await replaceAccountTagsAsync(tx, account.id, systemAccountId, tagNames, now)
    })
  } catch (error) {
    if (isDuplicateAccountNameError(error)) {
      throw new Error(`同一用户下账户名称已存在：${account.name}`)
    }
    throw error
  }

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
  const nextClientCompatibility = normalizeOpenAIAccountClientCompatibility(
    current.providerCode,
    current.type,
    hasOwnInput(input, 'clientCompatibility') ? input.clientCompatibility : current.clientCompatibility,
    current.clientCompatibility,
    current
  )
  const credentials = hasOwnInput(input, 'credentials')
    ? normalizeAccountCredentialsForWrite(current.type, input.credentials, {
      providerCode: current.providerCode,
      accountType: current.type,
      clientCompatibility: nextClientCompatibility,
      providerProtocolProfileId: current.providerProtocolProfileId,
      protocolCode: current.protocolCode,
      protocolVersion: current.protocolVersion
    })
    : normalizeAccountCredentialsForWrite(current.type, current.credentials, {
      providerCode: current.providerCode,
      accountType: current.type,
      clientCompatibility: nextClientCompatibility,
      providerProtocolProfileId: current.providerProtocolProfileId,
      protocolCode: current.protocolCode,
      protocolVersion: current.protocolVersion
    })
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
    ? normalizeAccountModelMappingsForProvider(input.modelMappings, current.providerCode, systemAccountId, current, {
        supportedEndpointModes: credentials.supported_endpoint_modes as AccountSupportedEndpointMode[]
      }) ?? []
    : current.modelMappings ?? []
  assertAccountModelMappingUpstreamsAllowedBySupportedModels(nextModelMappings, nextSupportedModels)
  const hasTagsInput = hasOwnInput(input, 'tags')
  const nextTagNames = hasTagsInput
    ? normalizeAccountTagNamesInput(input.tags) ?? []
    : (current.tags ?? []).map((tag) => tag.name)
  const hasAvailabilityScheduleInput = isAccountAvailabilityScheduleInputPresent(input)
  const hasAvailabilityScheduleActiveInput = hasOwnInput(input, 'availabilityScheduleActive')
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
  assertAccountEndpointModesCompatible(current, {
    modes: credentials.supported_endpoint_modes as AccountSupportedEndpointMode[],
    modelMappings: nextModelMappings,
    accountType: current.type,
    clientCompatibility: nextClientCompatibility
  })

  const requestedSchedulable = normalizeOptionalBooleanInput(input, 'schedulable', current.schedulable, '账户是否参与调度')
  const updateNowMs = Date.now()
  const next: AccountSummary = accountSummaryWithEffectiveAvailability({
    ...current,
    name: normalizeOptionalAccountNameInput(input, current.name),
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
    availabilityScheduleActive: nextAccountAvailabilityScheduleActiveValue({
      currentActive: current.availabilityScheduleActive === true,
      hasScheduleInput: hasAvailabilityScheduleInput,
      hasScheduleActiveInput: hasAvailabilityScheduleActiveInput,
      hasStatusInput,
      nextStatus,
      nextSchedule: nextAvailabilitySchedule,
      scheduleActiveInput: input.availabilityScheduleActive,
      now: new Date(updateNowMs)
    }),
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
  const availabilityScheduleNextCheckAt = nextAccountAvailabilityScheduleCheckAt(next.availabilitySchedule, new Date(updateNowMs))
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
            priority = ?, super_priority_enabled = ?, fallback_enabled = ?, client_compatibility = ?, schedulable = ?, availability_schedule_json = ?, availability_schedule_active = ?, availability_schedule_next_check_at = ?, account_expires_at = ?, cooldown_until = ?, last_error_code = ?, last_error_message = ?,
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
        next.availabilityScheduleActive ? 1 : 0,
        availabilityScheduleNextCheckAt,
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
      replaceAccountNameSearchTerms(database, id, systemAccountId, next.name, updatedAt)
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

export async function updateAccountAsync(id: string, input: Record<string, unknown>, access?: AccessScope): Promise<AccountSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return updateAccount(id, input, access)
  }
  assertKnownInputKeys(input, accountUpdateInputKeys, '账户更新参数')
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const current = await findAccountForTestAsync(id, access)
  if (!current) {
    return undefined
  }
  if (current.accessType === 'authorized' || current.accountAuthorizationId) {
    return undefined
  }
  const systemAccountId = current.ownerSystemAccountId
  if (!systemAccountId) {
    throw new Error('账户归属数据异常，请清理后再编辑')
  }
  if (!canManageResourceOwner(systemAccountId, access)) {
    return undefined
  }
  const nextClientCompatibility = normalizeOpenAIAccountClientCompatibility(
    current.providerCode,
    current.type,
    hasOwnInput(input, 'clientCompatibility') ? input.clientCompatibility : current.clientCompatibility,
    current.clientCompatibility,
    current
  )
  const credentials = hasOwnInput(input, 'credentials')
    ? normalizeAccountCredentialsForWrite(current.type, input.credentials, {
      providerCode: current.providerCode,
      accountType: current.type,
      clientCompatibility: nextClientCompatibility,
      providerProtocolProfileId: current.providerProtocolProfileId,
      protocolCode: current.protocolCode,
      protocolVersion: current.protocolVersion
    })
    : normalizeAccountCredentialsForWrite(current.type, current.credentials, {
      providerCode: current.providerCode,
      accountType: current.type,
      clientCompatibility: nextClientCompatibility,
      providerProtocolProfileId: current.providerProtocolProfileId,
      protocolCode: current.protocolCode,
      protocolVersion: current.protocolVersion
    })
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
    ? await normalizeAccountSupportedModelsForProviderAsync(input.supportedModels, current.providerCode, systemAccountId) ?? []
    : current.supportedModels ?? []
  const hasModelMappingsInput = hasOwnInput(input, 'modelMappings')
  const nextModelMappings = hasModelMappingsInput
    ? await normalizeAccountModelMappingsForProviderAsync(input.modelMappings, current.providerCode, systemAccountId, current, {
        supportedEndpointModes: credentials.supported_endpoint_modes as AccountSupportedEndpointMode[]
      }) ?? []
    : current.modelMappings ?? []
  assertAccountModelMappingUpstreamsAllowedBySupportedModels(nextModelMappings, nextSupportedModels)
  const hasTagsInput = hasOwnInput(input, 'tags')
  const nextTagNames = hasTagsInput
    ? normalizeAccountTagNamesInput(input.tags) ?? []
    : (current.tags ?? []).map((tag) => tag.name)
  const hasAvailabilityScheduleInput = isAccountAvailabilityScheduleInputPresent(input)
  const hasAvailabilityScheduleActiveInput = hasOwnInput(input, 'availabilityScheduleActive')
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
  let nextFallbackEnabled = requestedFallback
  if (hasSuperPriorityInput && requestedSuperPriority && hasFallbackInput && requestedFallback) {
    throw new Error('超级优先和降级备用不能同时开启')
  }
  if (hasSuperPriorityInput && nextSuperPriorityEnabled) {
    nextFallbackEnabled = false
  }
  if (hasFallbackInput && nextFallbackEnabled) {
    nextSuperPriorityEnabled = false
  }
  assertAccountEndpointModesCompatible(current, {
    modes: credentials.supported_endpoint_modes as AccountSupportedEndpointMode[],
    modelMappings: nextModelMappings,
    accountType: current.type,
    clientCompatibility: nextClientCompatibility
  })

  const requestedSchedulable = normalizeOptionalBooleanInput(input, 'schedulable', current.schedulable, '账户是否参与调度')
  const updateNowMs = Date.now()
  const requestedProxyProfileId = hasOwnInput(input, 'proxyProfileId')
    ? normalizeNullableIdInput(input.proxyProfileId, '代理配置')
    : current.proxyProfileId
  const proxyProfileId = await resolveEnabledProxyProfileIdForAccountWriteAsync(client, requestedProxyProfileId)
  const next: AccountSummary = accountSummaryWithEffectiveAvailability({
    ...current,
    name: normalizeOptionalAccountNameInput(input, current.name),
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
    proxyProfileId,
    schedulable: expiredByPackage || nextStatus !== 'active' || isHardUnavailableAccountStatus(nextStatus)
      ? false
      : hasStatusInput
        ? true
        : requestedSchedulable,
    availabilitySchedule: nextAvailabilitySchedule,
    availabilityScheduleActive: nextAccountAvailabilityScheduleActiveValue({
      currentActive: current.availabilityScheduleActive === true,
      hasScheduleInput: hasAvailabilityScheduleInput,
      hasScheduleActiveInput: hasAvailabilityScheduleActiveInput,
      hasStatusInput,
      nextStatus,
      nextSchedule: nextAvailabilitySchedule,
      scheduleActiveInput: input.availabilityScheduleActive,
      now: new Date(updateNowMs)
    }),
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

  await assertAccountNameAvailableAsync(client, systemAccountId, next.name, id)
  const updatedAt = nowIso()
  const availabilityScheduleNextCheckAt = nextAccountAvailabilityScheduleCheckAt(next.availabilitySchedule, new Date(updateNowMs))
  let renamedAuthorizationInstanceIds: string[] = []
  let savedTags = next.tags ?? []
  try {
    await client.transaction(async (tx) => {
      const result = await tx.execute(`
        UPDATE ${accountWriteTable(tx, 'accounts')}
        SET name = ?, notes = ?, status = ?, credentials_encrypted = ?, credential_fingerprint = ?, credential_mask = ?,
            oauth_access_token_expires_at = ?, oauth_refresh_token_present = ?,
            proxy_profile_id = ?, concurrency_limit = ?,
            priority = ?, super_priority_enabled = ?, fallback_enabled = ?, client_compatibility = ?, schedulable = ?, availability_schedule_json = ?, availability_schedule_active = ?, availability_schedule_next_check_at = ?, account_expires_at = ?, cooldown_until = ?, last_error_code = ?, last_error_message = ?,
            cooldown_retest_failure_count = ?, cooldown_retest_observation_started_at = ?, cooldown_retest_last_at = ?, cooldown_retest_last_status_code = ?, updated_at = ?
        WHERE id = ?
          AND system_account_id = ?
          AND deleted_at IS NULL
      `, [
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
        next.availabilityScheduleActive ? 1 : 0,
        availabilityScheduleNextCheckAt,
        next.accountExpiresAt ?? null,
        next.cooldownUntil ?? null,
        next.lastErrorCode ?? null,
        next.lastErrorMessage ?? null,
        next.cooldownRetestFailureCount ?? 0,
        nextCooldownRetestObservationStartedAt ?? null,
        next.cooldownRetestLastAt ?? null,
        next.cooldownRetestLastStatusCode ?? null,
        updatedAt,
        id,
        systemAccountId
      ])
      if (Number(result.changes ?? 0) <= 0) {
        return
      }
      if (next.name !== current.name) {
        await replaceAccountNameSearchTermsAsync(tx, id, systemAccountId, next.name, updatedAt)
        renamedAuthorizationInstanceIds = await syncAccountAuthorizationInstanceNamesForSourceAccountAsync(tx, id, next.name, updatedAt)
      }
      if (hasSupportedModelsInput) {
        await replaceAccountSupportedModelsInClientAsync(tx, id, next.providerCode, nextSupportedModels)
      }
      if (hasModelMappingsInput) {
        await replaceAccountModelMappingsInClientAsync(tx, id, next.providerCode, nextModelMappings)
      }
      if (hasTagsInput) {
        savedTags = await replaceAccountTagsAsync(tx, id, systemAccountId, nextTagNames, updatedAt)
      }
      if (hasPriorityInput || hasSuperPriorityInput || hasFallbackInput) {
        await tx.execute(`
          UPDATE ${accountWriteTable(tx, 'group_accounts')}
          SET local_priority = ?,
              local_super_priority_enabled = ?,
              local_fallback_enabled = ?,
              updated_at = ?
          WHERE account_id = ?
            AND system_account_id = ?
            AND enabled = 1
        `, [
          next.priority,
          next.superPriorityEnabled ? 1 : 0,
          next.fallbackEnabled ? 1 : 0,
          updatedAt,
          id,
          systemAccountId
        ])
      }
    })
  } catch (error) {
    if (isDuplicateAccountNameError(error)) {
      throw new Error(`同一用户下账户名称已存在：${next.name}`)
    }
    throw error
  }

  invalidateAccountLookupCache(id)
  for (const instanceId of renamedAuthorizationInstanceIds) {
    invalidateAccountLookupCache(instanceId)
  }
  invalidateGatewayRuntimeAfterBusinessWrite('account_updated')

  return { ...next, tags: savedTags }
}

async function syncAccountAuthorizationInstanceNamesForSourceAccountAsync(client: DatabaseClient, sourceAccountId: string, sourceName: string, now = nowIso()): Promise<string[]> {
  const rows = await client.query<{
    id?: string
    system_account_id?: string
    authorization_instance_authorization_id?: string | null
    name?: string
  }>(`
    SELECT id, system_account_id, authorization_instance_authorization_id, name
    FROM ${accountWriteTable(client, 'accounts')}
    WHERE authorization_instance_source_account_id = ?
      AND deleted_at IS NULL
    ORDER BY created_at ASC, id ASC
  `, [sourceAccountId])
  const changedIds: string[] = []
  for (const row of rows) {
    if (!row.id || !row.system_account_id || !row.authorization_instance_authorization_id) continue
    const nextName = await uniqueAuthorizedAccountInstanceNameAsync(
      client,
      sourceName,
      row.system_account_id,
      row.authorization_instance_authorization_id,
      row.id
    )
    if (row.name === nextName) continue
    await client.execute(`UPDATE ${accountWriteTable(client, 'accounts')} SET name = ?, updated_at = ? WHERE id = ?`, [nextName, now, row.id])
    await replaceAccountNameSearchTermsAsync(client, row.id, row.system_account_id, nextName, now)
    changedIds.push(row.id)
  }
  return changedIds
}

async function uniqueAuthorizedAccountInstanceNameAsync(client: DatabaseClient, sourceName: string, systemAccountId: string, authorizationId: string, exceptAccountId?: string): Promise<string> {
  const baseName = sourceName.trim() || '授权账户'
  const shortId = authorizationId.split('_').pop()?.slice(0, 6) || authorizationId.slice(-6)
  const candidates = [
    baseName,
    `${baseName}-${shortId}`
  ]
  for (const candidate of candidates) {
    if (await isAccountNameAvailableAsync(client, systemAccountId, candidate, exceptAccountId)) return candidate
  }
  for (let index = 2; index <= 1000; index += 1) {
    const candidate = `${baseName}-${shortId}-${index}`
    if (await isAccountNameAvailableAsync(client, systemAccountId, candidate, exceptAccountId)) return candidate
  }
  return `${baseName}-${shortId}-${Date.now()}`
}

async function isAccountNameAvailableAsync(client: DatabaseClient, systemAccountId: string, name: string, exceptAccountId?: string): Promise<boolean> {
  const params: string[] = [systemAccountId, name]
  const exceptClause = exceptAccountId ? ' AND id <> ?' : ''
  if (exceptAccountId) {
    params.push(exceptAccountId)
  }
  const row = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${accountWriteTable(client, 'accounts')}
    WHERE system_account_id = ?
      AND lower(name) = lower(?)
      AND deleted_at IS NULL${exceptClause}
    LIMIT 1
  `, params)
  return !row?.id
}

function nextAccountAvailabilityScheduleActiveValue(input: {
  currentActive: boolean
  hasScheduleInput: boolean
  hasScheduleActiveInput: boolean
  hasStatusInput: boolean
  nextStatus: AccountStatus
  nextSchedule: AccountSummary['availabilitySchedule']
  scheduleActiveInput: unknown
  now: Date
}): boolean {
  if (!input.nextSchedule?.enabled) {
    return true
  }
  if (input.hasScheduleActiveInput) {
    return normalizeAccountAvailabilityScheduleActiveOverride(input.scheduleActiveInput, input.nextSchedule)
  }
  if (input.hasScheduleInput) {
    return isAccountAvailabilityScheduleAllowed(accountAvailabilityScheduleJson(input.nextSchedule), input.now)
  }
  if (input.hasStatusInput && input.nextStatus === 'active') {
    return true
  }
  return input.currentActive
}

function normalizeAccountAvailabilityScheduleActiveOverride(
  value: unknown,
  schedule: AccountSummary['availabilitySchedule']
): boolean {
  if (!schedule?.enabled) {
    throw new Error('只有启用时间计划的账户才可以调整时间计划派生状态')
  }
  if (typeof value !== 'boolean') {
    throw new Error('账户时间计划派生状态必须是布尔值')
  }
  return value
}

export function deleteAccount(id: string, access?: AccessScope): boolean {
  return deleteAccountWithRelatedCleanup(id, access).deleted
}

export async function deleteAccountAsync(id: string, access?: AccessScope): Promise<boolean> {
  return (await deleteAccountWithRelatedCleanupAsync(id, access)).deleted
}

export {
  cleanupExpiredLogicallyDeletedAccounts,
  deleteAccountWithRelatedCleanupAsync,
  deleteAccountWithRelatedCleanup,
  type AccountDeleteResult,
  type ExpiredDeletedAccountCleanupOptions,
  type ExpiredDeletedAccountCleanupResult
} from './account-delete-cleanup.repository.js'

export {
  clearAccountFailureState,
  clearAccountFailureStateAsync,
  clearAccountFailureStateResult,
  clearAccountFailureStateResultAsync,
  clearAccountStreamFailureState,
  clearAuthorizedAccountBindingFailureState,
  clearAuthorizedAccountBindingFailureStateByContext,
  clearAuthorizedAccountBindingStreamFailureState,
  getAccountPrecheckMutationState,
  markAccountCooldown,
  markAccountDisabledByFailure,
  markAccountException,
  markAccountTemporaryUnavailable,
  markAccountTestTemporaryUnavailable,
  markAuthorizedAccountBindingCooldownByContext,
  markAuthorizedAccountBindingDisabledByFailure,
  markAuthorizedAccountBindingTemporaryUnavailableByContext,
  migrateAccountTraffic,
  recordAccountStreamFailure,
  recordAuthorizedAccountBindingStreamFailure,
  updateAuthorizedAccountBindingDispatch,
  type AccountFailureStateClearResult,
  type AccountPrecheckMutationState,
  type AuthorizedAccountBindingRuntimeTarget
} from './account-runtime-mutation.repository.js'

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
