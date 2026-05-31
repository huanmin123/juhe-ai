import type { AccountGroupBindStatus, AccountStatus, AccountTrafficMigrationSourceStatus, AccountType, AuthorizationStatus, GroupType, ProviderCode, ResourceAccessType } from './base'
import type { ApiKeyAvailabilitySchedule, RequestQuotaLimits } from './access'
import type { AuthorizationSourceSummary } from './authorizations'
import type { AccountUsageSummary } from './usage-stats'

export interface AccountCredentials {
  api_key?: string
  base_url?: string
  access_token?: string
  refresh_token?: string
  client_id?: string
  expires_at?: string
  account_id?: string
  [key: string]: unknown
}

export interface ResourcePermissions {
  canUse: boolean
  canEdit: boolean
  canDelete: boolean
  canAuthorize: boolean
  canViewCredentials: boolean
  canManageAccounts?: boolean
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

export type AccountRuntimeAvailabilityStatus = 'normal' | 'local_suppressed' | 'precheck_pending' | 'precheck_failed'

export interface AccountRuntimeAvailability {
  status: AccountRuntimeAvailabilityStatus
  reason?: string
  since?: string
  until?: string
  failureCount?: number
  distinctClientIpCount?: number
  distinctApiKeyCount?: number
  precheckAttemptCount?: number
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

export interface AccountSummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  providerCode: ProviderCode
  name: string
  notes?: string
  type: AccountType
  credentials: AccountCredentials
  status: AccountStatus
  concurrencyLimit: number
  currentConcurrency: number
  currentConcurrencyAvailable?: boolean
  runtimeAvailability?: AccountRuntimeAvailability
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  supportedModels?: string[]
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
  passthroughEnabled: boolean
  errorPolicyId?: string
  schedulable: boolean
  availabilitySchedule?: ApiKeyAvailabilitySchedule
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
  /** @deprecated 仅兼容旧接口字段；授权实例运行态以 status / schedulable / cooldownUntil 为准，不能用来源账户状态做业务判断。 */
  sourceStatus?: AccountStatus
  /** @deprecated 仅兼容旧接口字段；授权实例运行态以 status / schedulable / cooldownUntil 为准，不能用来源账户状态做业务判断。 */
  sourceSchedulable?: boolean
  /** @deprecated 仅兼容旧接口字段；授权实例运行态以 status / schedulable / cooldownUntil 为准，不能用来源账户状态做业务判断。 */
  sourceCooldownUntil?: string
  /** @deprecated 仅兼容旧接口字段；授权实例运行态以 status / schedulable / cooldownUntil 为准，不能用来源账户状态做业务判断。 */
  sourceLastErrorCode?: string
  /** @deprecated 仅兼容旧接口字段；授权实例运行态以 status / schedulable / cooldownUntil 为准，不能用来源账户状态做业务判断。 */
  sourceLastErrorMessage?: string
  /** @deprecated 仅兼容旧接口字段；授权实例运行态以 status / schedulable / cooldownUntil 为准，分组内优先级使用 priority / superPriorityEnabled / fallbackEnabled。 */
  localStatus?: AccountStatus
  /** @deprecated 仅兼容旧接口字段；授权实例运行态以 status / schedulable / cooldownUntil 为准，分组内优先级使用 priority / superPriorityEnabled / fallbackEnabled。 */
  localCooldownUntil?: string
  /** @deprecated 仅兼容旧接口字段；授权实例运行态以 status / schedulable / cooldownUntil 为准，分组内优先级使用 priority / superPriorityEnabled / fallbackEnabled。 */
  localLastErrorMessage?: string
  lastUsedAt?: string
  todayUsage: AccountUsageSummary
  usage: AccountUsageSummary
  oauthUsage?: AccountOAuthUsageSnapshot
  accessType?: ResourceAccessType
  accountAuthorizationId?: string
  authorizationInstanceSourceAccountId?: string
  authorizationInstanceOwnerSystemAccountId?: string
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
  type: AccountType
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

export interface ErrorPolicySummary {
  id: string
  name: string
  enabled: boolean
  rules: Array<Record<string, unknown>>
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
}

export interface GroupSummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  name: string
  providerCode: ProviderCode
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
