import type { AccountGroupBindStatus, AccountStatus, AccountTrafficMigrationSourceStatus, AccountType, AuthorizationStatus, GroupType, ProviderCode, ResourceAccessType } from './base'
import type { RequestQuotaLimits } from './access'
import type { AuthorizationSourceSummary } from './authorizations'
import type { AccountUsageSummary } from './usage-stats'

export type AccountClientCompatibility = 'openai_standard' | 'codex_responses'

export interface AccountCredentials {
  api_key?: string
  base_url?: string
  access_token?: string
  refresh_token?: string
  client_id?: string
  id_token?: string
  email?: string
  expires_at?: string
  account_id?: string
  chatgpt_user_id?: string
  plan_type?: string
  stream_intercept_rules?: unknown[]
  [key: string]: unknown
}

export interface ResourcePermissions {
  canUse: boolean
  canEdit: boolean
  canDelete: boolean
  canReturnAuthorization?: boolean
  canAuthorize: boolean
  canViewCredentials: boolean
  canManageAccounts?: boolean
  canBindToApiKey?: boolean
}

export interface AccountOAuthUsageWindow {
  utilization: number
  resetsAt?: string
  remainingSeconds: number
  windowMinutes?: number
}

export interface AccountOAuthUsageSnapshot {
  kind: 'openai_codex'
  source?: string
  updatedAt?: string
  refreshStatus?: string
  lastAttemptAt?: string
  lastSuccessAt?: string
  nextRefreshAfter?: string
  lastErrorMessage?: string
  fiveHour?: AccountOAuthUsageWindow
  sevenDay?: AccountOAuthUsageWindow
}

export type AccountRuntimeAvailabilityStatus = 'normal' | 'local_suppressed' | 'half_open' | 'precheck_pending' | 'precheck_failed'

export interface AccountRuntimeAvailability {
  status: AccountRuntimeAvailabilityStatus
  reason?: string
  since?: string
  until?: string
  failureCount?: number
  distinctClientIpCount?: number
  distinctApiKeyCount?: number
  precheckAttemptCount?: number
  localFailureCount?: number
}

export type AccountEffectiveAvailabilityStatus =
  | 'available'
  | 'permission_denied'
  | 'authorization_expired'
  | 'authorization_paused'
  | 'authorization_unavailable'
  | 'authorization_quota_exceeded'
  | 'source_deleted'
  | 'source_expired'
  | 'source_pending_test'
  | 'source_disabled'
  | 'source_error'
  | 'source_rate_limited'
  | 'source_temporary_unavailable'
  | 'source_cooldown'
  | 'source_unschedulable'
  | 'source_schedule_inactive'
  | 'instance_expired'
  | 'instance_pending_test'
  | 'instance_disabled'
  | 'instance_error'
  | 'instance_rate_limited'
  | 'instance_temporary_unavailable'
  | 'instance_cooldown'
  | 'instance_unschedulable'
  | 'instance_schedule_inactive'
  | 'binding_missing'
  | 'runtime_local_suppressed'
  | 'runtime_half_open'
  | 'runtime_precheck_pending'
  | 'runtime_precheck_failed'

export type AccountEffectiveAvailabilityBlockerScope =
  | 'permission'
  | 'authorization'
  | 'source_account'
  | 'account'
  | 'authorized_instance'
  | 'binding'
  | 'runtime'

export interface AccountEffectiveAvailability {
  available: boolean
  status: AccountEffectiveAvailabilityStatus
  label: string
  color: string
  blockerScope?: AccountEffectiveAvailabilityBlockerScope
  reason?: string
  retryAt?: string
}

export type AccountAvailabilityScheduleMode = 'allow_windows'
export type AccountAvailabilityScheduleExceptionAction = 'allow' | 'deny'

export interface AccountAvailabilityScheduleWindow {
  daysOfWeek: number[]
  start: string
  end: string
}

export type AccountAvailabilityScheduleException =
  | {
    date: string
    action: 'allow'
    windows: Array<Pick<AccountAvailabilityScheduleWindow, 'start' | 'end'>>
  }
  | {
    date: string
    action: 'deny'
    windows?: never
  }

export interface AccountAvailabilitySchedule {
  enabled: boolean
  timezone: string
  mode: AccountAvailabilityScheduleMode
  windows: AccountAvailabilityScheduleWindow[]
  dateRange?: {
    startDate?: string
    endDate?: string
  }
  exceptions?: AccountAvailabilityScheduleException[]
}

export interface GroupAccountStats {
  total: number
  available: number
  active: number
  disabled: number
  error: number
  rateLimited: number
  currentConcurrency: number
  currentConcurrencyAvailable?: boolean
  concurrencyLimit: number
  todayUsage: AccountUsageSummary
  usage: AccountUsageSummary
}

export interface AccountModelMapping {
  sourceModel: string
  upstreamModel: string
  enabled: boolean
}

export interface AccountTagSummary {
  id: string
  name: string
  accountCount?: number
  createdAt?: string
  updatedAt?: string
}

export interface AccountSummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  providerCode: ProviderCode
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  name: string
  notes?: string
  type: AccountType
  credentials: AccountCredentials
  status: AccountStatus
  concurrencyLimit: number
  currentConcurrency: number
  currentConcurrencyAvailable?: boolean
  runtimeAvailability?: AccountRuntimeAvailability
  effectiveAvailability?: AccountEffectiveAvailability
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  clientCompatibility: AccountClientCompatibility
  supportedModels?: string[]
  modelMappings?: AccountModelMapping[]
  tags?: AccountTagSummary[]
  lastSuccessfulTestModel?: string
  qualityScore?: number
  qualityState?: string
  qualityEwmaFirstTokenMs?: number
  qualityRecentAvgFirstTokenMs?: number
  qualityRecentRequestCount?: number
  qualityRecentSuccessRate?: number
  qualityUpdatedAt?: string
  proxyProfileId?: string
  proxyProfileUnavailable?: boolean
  proxyProfileErrorMessage?: string
  schedulable: boolean
  availabilitySchedule?: AccountAvailabilitySchedule
  availabilityScheduleActive?: boolean
  accountExpiresAt?: string
  cooldownUntil?: string
  lastErrorCode?: string
  lastErrorMessage?: string
  cooldownRetestFailureCount?: number
  cooldownRetestObservationStartedAt?: string
  cooldownRetestLastAt?: string
  cooldownRetestLastStatusCode?: number
  streamFailureCount?: number
  streamFailureWindowStartedAt?: string
  lastUsedAt?: string
  todayUsage: AccountUsageSummary
  usage: AccountUsageSummary
  oauthUsage?: AccountOAuthUsageSnapshot
  accessType?: ResourceAccessType
  accountAuthorizationId?: string
  authorizationInstanceSourceAccountId?: string
  authorizationInstanceOwnerSystemAccountId?: string
  authorizationInstanceSourceAccountStatus?: AccountStatus
  authorizationInstanceSourceAccountSchedulable?: boolean
  authorizationInstanceSourceAccountAvailabilitySchedule?: AccountAvailabilitySchedule
  authorizationInstanceSourceAccountScheduleActive?: boolean
  authorizationInstanceSourceAccountExpiresAt?: string
  authorizationInstanceSourceAccountCooldownUntil?: string
  authorizationInstanceSourceAccountLastErrorCode?: string
  authorizationInstanceSourceAccountLastErrorMessage?: string
  boundGroupId?: string
  boundGroupName?: string
  groupBindStatus?: AccountGroupBindStatus
  bindingSystemAccountId?: string
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  authorizationStatus?: AuthorizationStatus
  authorizationExpiresAt?: string
  authorizationLimits?: RequestQuotaLimits
  authorizationQuotaExceeded?: boolean
  authorizationSources?: AuthorizationSourceSummary[]
  permissions?: ResourcePermissions
  authorizationUsageAvailable?: boolean
  authorizationCount?: number
  authorizationTeamCount?: number
}

export interface AccountListResult {
  items: AccountSummary[]
  total: number
  hasMore?: boolean
  page: number
  pageSize: number
  runtimeSnapshot?: {
    accountConcurrencyAvailable: boolean
    accountRuntimeAvailabilityAvailable?: boolean
  }
}

export type AccountOptionSummary = Pick<
  AccountSummary,
  | 'id'
  | 'systemAccountId'
  | 'systemAccountName'
  | 'ownerSystemAccountId'
  | 'ownerSystemAccountName'
  | 'providerCode'
  | 'providerProtocolProfileId'
  | 'protocolCode'
  | 'protocolVersion'
  | 'name'
  | 'type'
  | 'status'
  | 'accessType'
  | 'accountAuthorizationId'
  | 'authorizationStatus'
  | 'authorizationExpiresAt'
  | 'accountExpiresAt'
  | 'permissions'
>

export interface AccountTestResult {
  accountId: string
  accountName: string
  providerCode: ProviderCode
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  type: AccountType
  traceId?: string
  success: boolean
  statusCode?: number
  errorCode?: string
  message: string
  model?: string
  requestUrl?: string
  requestBody?: unknown
  responseHeaders?: Record<string, string | string[]>
  responseBody?: unknown
  responseText?: string
  responseTruncated?: boolean
  outputText?: string
  modelsUrl?: string
  proxyUrl?: string
  tokenRefreshed?: boolean
  durationMs?: number
  firstTokenMs?: number
  accountStatusChanged?: boolean
  accountStatus?: AccountStatus
  accountFailureEligible?: boolean
  errorPolicyAction?: 'none' | 'retry_next' | 'cooldown' | 'disable'
  errorPolicyReason?: string
  clientCompatibility?: AccountClientCompatibility
  testClientCompatibility?: AccountClientCompatibility
}

export type AccountTestTaskStatus = 'queued' | 'running' | 'success' | 'failed' | 'canceled'

export interface AccountTestTask {
  id: string
  accountId: string
  accountName: string
  providerCode: ProviderCode
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  type: AccountType
  status: AccountTestTaskStatus
  message?: string
  model?: string
  clientCompatibility?: AccountClientCompatibility
  result?: AccountTestResult
  cancelRequested?: boolean
  createdAt: string
  queuedAt: string
  startedAt?: string
  finishedAt?: string
  updatedAt: string
}

export interface AccountTrafficMigrationResult {
  sourceAccount: AccountSummary
  targetAccount: AccountSummary
  migratedSessionCount: number
  sourceStatus: AccountTrafficMigrationSourceStatus
  sourceCooldownUntil?: string | null
}

export interface AccountImportOptions {
  createMissingGroups?: boolean
  createMissingProxies?: boolean
  skipDuplicates?: boolean
}

export interface AccountImportSummary {
  accounts: {
    total: number
    create: number
    skip: number
    failed: number
  }
  proxies: {
    total: number
    create: number
    reuse: number
    skip: number
    failed: number
  }
  groups: {
    create: number
    reuse: number
    failed: number
  }
}

export interface AccountImportItem {
  index: number
  ref?: string
  name?: string
  providerCode?: ProviderCode
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  accountType?: AccountType
  groupName?: string
  groupId?: string
  proxyRef?: string
  action: 'create' | 'reuse' | 'skip' | 'failed'
  messages: string[]
  warnings: string[]
  accountId?: string
}

export interface AccountImportProxyItem {
  index: number
  ref?: string
  name?: string
  action: 'create' | 'reuse' | 'skip' | 'failed'
  messages: string[]
  warnings: string[]
  proxyProfileId?: string
}

export interface AccountExportAccount {
  ref: string
  name: string
  providerCode: ProviderCode
  type: AccountType
  status: 'active' | 'pending_test' | 'disabled'
  groupId?: string
  groupName?: string
  proxyRef?: string
  concurrencyLimit?: number
  priority?: number
  superPriorityEnabled?: boolean
  fallbackEnabled?: boolean
  supportedModels?: string[]
  modelMappings?: AccountModelMapping[]
  tags?: string[]
  accountExpiresAt?: string
  availabilitySchedule?: AccountAvailabilitySchedule
  credentials: AccountCredentials
  notes?: string
}

export interface AccountImportResult {
  type: 'juhe-ai-account-import'
  version: 1
  mode: 'preview' | 'import'
  canImport: boolean
  imported: boolean
  summary: AccountImportSummary
  accounts: AccountImportItem[]
  proxies: AccountImportProxyItem[]
  messages: string[]
}

export interface AccountExportDocument {
  type: 'juhe-ai-account-import'
  version: 1
  proxies?: Array<Record<string, unknown>>
  accounts: AccountExportAccount[]
}

export interface AccountExportResult {
  document: AccountExportDocument
  summary: {
    accounts: number
    proxies: number
    skippedAccounts: number
    matchedAccounts?: number
    truncated?: boolean
  }
}

export interface GroupSchedulingPolicy {
  mode?: 'balanced_fast'
  defaultSoftConcurrency?: number
  fastFirstEnabled?: boolean
  fallbackOnQueueEnabled?: boolean
  breakAffinityOnSoftLimit?: boolean
  breakAffinityOnQueueWaitMs?: number
  slowRequestThresholdMs?: number
  firstOutputSlowThresholdMs?: number
  recentTimeoutWindowSeconds?: number
  recentTimeoutPenaltyThreshold?: number
  maxQueueWaitMs?: number
  maxQueueSize?: number
  perApiKeyQueueLimit?: number
  clientIpConcurrencyLimit?: number
  clientIpConcurrencyOverflowMode?: 'reject' | 'queue'
  imageLaneMaxConcurrency?: number
}

export interface GroupSummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  name: string
  providerCode: ProviderCode
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  description?: string
  enabled: boolean
  isDefault: boolean
  groupType: GroupType
  schedulingPolicy?: GroupSchedulingPolicy
  accountIds: string[]
  accountStats: GroupAccountStats
  accessType?: ResourceAccessType
  groupAuthorizationId?: string
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  authorizationStatus?: AuthorizationStatus
  authorizationExpiresAt?: string
  authorizationLimits?: RequestQuotaLimits
  authorizationSources?: AuthorizationSourceSummary[]
  permissions?: ResourcePermissions
}

export interface GroupListResult {
  items: GroupSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
  runtimeSnapshot?: {
    accountConcurrencyAvailable: boolean
  }
}

export type GroupOptionSummary = Pick<
  GroupSummary,
  | 'id'
  | 'systemAccountId'
  | 'systemAccountName'
  | 'ownerSystemAccountId'
  | 'ownerSystemAccountName'
  | 'name'
  | 'providerCode'
  | 'providerProtocolProfileId'
  | 'protocolCode'
  | 'protocolVersion'
  | 'enabled'
  | 'isDefault'
  | 'groupType'
  | 'schedulingPolicy'
  | 'accessType'
  | 'groupAuthorizationId'
  | 'authorizationExpiresAt'
  | 'authorizationLimits'
  | 'authorizationStatus'
  | 'permissions'
>

export interface AccountGroupOptionSummary extends GroupOptionSummary {
  accountIds: string[]
}
