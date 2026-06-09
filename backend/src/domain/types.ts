export type ProviderCode = string
export type AccountType = string
export type AccountStatus = 'active' | 'pending_test' | 'disabled' | 'error' | 'rate_limited' | 'temporary_unavailable'
export type AccountTrafficMigrationSourceStatus = 'temporary_unavailable' | 'disabled'
export const ACCOUNT_CLIENT_COMPATIBILITIES = ['openai_standard', 'codex_responses'] as const
export type AccountClientCompatibility = typeof ACCOUNT_CLIENT_COMPATIBILITIES[number]
export const SYSTEM_ACCOUNT_ROLES = ['super_admin', 'admin', 'user'] as const
export type SystemAccountRole = typeof SYSTEM_ACCOUNT_ROLES[number]
export type ManagementSystemAccountRole = Extract<SystemAccountRole, 'super_admin' | 'admin'>

export function isAdminRole(role: unknown): role is ManagementSystemAccountRole {
  return role === 'super_admin' || role === 'admin'
}

export function isSuperAdminRole(role: unknown): role is 'super_admin' {
  return role === 'super_admin'
}
export type SystemAccountStatus = 'active' | 'disabled'
export type ResourceAccessType = 'owner' | 'authorized'
export type AccountUsageAccessType = 'owner' | 'authorized' | 'account_authorized' | 'group_authorized'
export type GroupUsageAccessType = 'owner' | 'authorized'
export type GroupType = 'personal' | 'high_concurrency'
export type AuthorizationStatus = 'active' | 'paused' | 'expired' | 'revoked' | 'returned'
export type SystemTeamStatus = 'active' | 'disabled'
export type SystemTeamMemberStatus = 'active' | 'removed'
export type ResourceAuthorizationResourceType = 'account' | 'group'
export type ResourceAuthorizationSourceType = 'manual' | 'team'
export type ResourceAuthorizationSourceStatus = 'active' | 'superseded' | 'revoked'
export type ResourceAuthorizationGranteeType = 'system_account' | 'team'
export type AccountGroupBindStatus = 'bound' | 'authorization_unavailable'
export type AnnouncementLevel = 'critical' | 'warning' | 'info' | 'normal'
export type AnnouncementStatus = 'draft' | 'published' | 'archived'

export interface SystemAccountSummary {
  id: string
  username: string
  displayName: string
  description?: string
  role: SystemAccountRole
  status: SystemAccountStatus
  mustChangePassword: boolean
  imageGenerationEnabled: boolean
  lastLoginAt?: string
  createdAt: string
  updatedAt: string
}

export type SystemAccountPrincipalSummary = Pick<SystemAccountSummary, 'id' | 'username' | 'displayName' | 'status'>

export interface CurrentUserSummary {
  id: string
  username: string
  displayName: string
  role: SystemAccountRole
  mustChangePassword: boolean
}

export interface AnnouncementSummary {
  id: string
  title: string
  content: string
  level: AnnouncementLevel
  status: AnnouncementStatus
  createdBy?: string
  createdByName?: string
  updatedBy?: string
  updatedByName?: string
  publishedAt?: string
  readAt?: string
  createdAt: string
  updatedAt: string
}

export interface SystemTeamMemberSummary {
  id: string
  teamId: string
  systemAccountId: string
  systemAccountName?: string
  username?: string
  memberRole: 'member'
  status: SystemTeamMemberStatus
  joinedAt: string
  removedAt?: string
  createdAt: string
  updatedAt: string
}

export interface SystemTeamSummary {
  id: string
  name: string
  description?: string
  status: SystemTeamStatus
  memberCount: number
  activeMemberCount: number
  members?: SystemTeamMemberSummary[]
  createdBy: string
  createdAt: string
  updatedAt: string
}

export interface SystemTeamListResult {
  items: SystemTeamSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export type SystemTeamPrincipalSummary = Pick<SystemTeamSummary, 'id' | 'name' | 'status'>

export interface ResourceAuthorizationSourceSummary {
  id: string
  authorizationId: string
  sourceType: ResourceAuthorizationSourceType
  sourceTeamId?: string
  sourceTeamName?: string
  status: ResourceAuthorizationSourceStatus
  activatedAt?: string
  endedAt?: string
  endedReason?: string
  createdBy: string
  createdAt: string
  revokedBy?: string
  revokedAt?: string
  updatedAt: string
}


export interface ProviderDefinition {
  id: string
  code: ProviderCode
  name: string
  parentCode?: ProviderCode
  description?: string
  enabled: boolean
  defaultProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  baseUrl: string
  defaultTestModel: string
  accountTypes: AccountType[]
  capabilities: string[]
  protocolProfiles: ProviderProtocolProfileDefinition[]
}

export interface ProtocolEndpointFamilyDefinition {
  code: string
  name: string
  description?: string
}

export interface ProviderProtocolProfileDefinition {
  id: string
  providerCode: ProviderCode
  name: string
  description?: string
  enabled: boolean
  protocolCode: string
  protocolVersion: string
  baseUrl: string
  defaultTestModel: string
  accountTypes: AccountType[]
  capabilities: string[]
  endpointFamilies: ProtocolEndpointFamilyDefinition[]
}

export interface ProviderModelPricing {
  providerCode: ProviderCode
  model: string
  id?: string
  scope?: 'built_in' | 'global' | 'personal'
  visibility?: 'public' | 'mapping_target_only'
  status?: 'draft' | 'active' | 'disabled'
  systemAccountId?: string
  displayName?: string
  pricingModel?: string
  mode?: string
  releaseDate?: string
  shutdownDate?: string
  contextWindowTokens?: number
  supportedApiProtocols: Array<'chat_completions' | 'responses' | 'completions' | 'images' | 'audio' | 'realtime'>
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
  imageInputUsdPer1M?: number
  imageOutputUsdPer1M?: number
  audioInputUsdPer1M?: number
  audioOutputUsdPer1M?: number
  outputUsdPerImage?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  maxTokens?: number
  supportsPromptCaching: boolean
  supportsServiceTier: boolean
  pricingNotes?: string
  capabilityNotes?: string
  notes?: string
  createdAt?: string
  updatedAt?: string
  source: string
}

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

export interface AccountUsageSummary {
  requestCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheReadCost: number
  totalTokens: number
  totalCost: number
  lastUsedAt?: string
}

export interface AiPerformanceAccount {
  id: string
  name: string
  status: AccountStatus
  providerCode: ProviderCode
  systemAccountId: string
  systemAccountName?: string
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  accessType?: ResourceAccessType
  requestCountLast7d: number
  selected: boolean
  defaultVisible: boolean
}

export interface AiPerformanceAccountOption {
  id: string
  name: string
  status: AccountStatus
  providerCode: ProviderCode
  systemAccountId: string
  systemAccountName?: string
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  accessType?: ResourceAccessType
  requestCountLast7d: number
}

export interface AiPerformancePoint {
  statHour: string
  requestCount: number
  firstTokenCount: number
  averageFirstTokenMs?: number
  maxFirstTokenMs?: number
  durationCount: number
  averageDurationMs?: number
  maxDurationMs?: number
}

export interface AiPerformanceAccountSeries {
  accountId: string
  accountName: string
  systemAccountId: string
  points: AiPerformancePoint[]
}

export interface AiPerformanceOverview {
  range: AccountUsageStatsRange
  defaultAccounts: AiPerformanceAccount[]
  selectedAccounts: AiPerformanceAccount[]
  accounts: AiPerformanceAccount[]
  hourlySeries: AiPerformanceAccountSeries[]
  summary: {
    requestCount: number
    firstTokenCount: number
    averageFirstTokenMs?: number
    maxFirstTokenMs?: number
    durationCount: number
    averageDurationMs?: number
    maxDurationMs?: number
  }
  statsLagSeconds?: number
}

export interface AccountUsageStatsRange {
  startDate: string
  endDate: string
  days: number
  maxDays: number
}

export interface AccountUsageDailyPoint extends AccountUsageSummary {
  statDate: string
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
  | 'source_disabled'
  | 'source_pending_test'
  | 'source_error'
  | 'source_rate_limited'
  | 'source_temporary_unavailable'
  | 'source_cooldown'
  | 'source_unschedulable'
  | 'source_schedule_inactive'
  | 'instance_expired'
  | 'instance_disabled'
  | 'instance_pending_test'
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

export interface AccountModelMapping {
  sourceModel: string
  upstreamModel: string
  enabled: boolean
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
  effectiveAvailability: AccountEffectiveAvailability
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  clientCompatibility: AccountClientCompatibility
  supportedModels?: string[]
  modelMappings?: AccountModelMapping[]
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
  authorizationSources?: ResourceAuthorizationSourceSummary[]
  permissions?: ResourcePermissions
  authorizationUsageAvailable?: boolean
  authorizationCount?: number
  authorizationTeamCount?: number
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

export interface AccountTrafficMigrationResult {
  sourceAccount: AccountSummary
  targetAccount: AccountSummary
  migratedSessionCount: number
  sourceStatus: AccountTrafficMigrationSourceStatus
  sourceCooldownUntil?: string | null
}

export interface AccountUsageStatsRow {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  ownerSystemAccountId: string
  ownerSystemAccountName?: string
  providerCode: ProviderCode
  name: string
  type: AccountType
  status: AccountStatus
  accessType?: ResourceAccessType
  rangeUsage: AccountUsageSummary
  dailyUsage: AccountUsageDailyPoint[]
  authorizationUsageAvailable: boolean
  authorizationCount: number
  authorizationTeamCount: number
}

export interface AccountUsageStatsOverview {
  range: AccountUsageStatsRange
  summary: AccountUsageSummary
  rows: AccountUsageStatsRow[]
  defaultTrendAccountIds: string[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
  statsLagSeconds?: number
}

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

export type ModelCheckTargetType = 'account'
export type ModelCheckProfile = 'full'
export type ModelCheckLevel = 'high_confidence' | 'likely' | 'uncertain' | 'suspicious' | 'unavailable'
export type ModelCheckRunStatus = 'running' | 'completed' | 'failed' | 'canceled'
export type ModelCheckItemStatus = 'passed' | 'warning' | 'failed' | 'skipped'

export interface ModelCheckSupportedOption {
  value: string
  label: string
  description?: string
}

export interface ModelCheckTrustedComparisonStatus {
  enabledByDefault: boolean
  available: boolean
  unavailableReason?: string
  message?: string
}

export interface ModelCheckOptions {
  supportedModels: ModelCheckSupportedOption[]
  supportedProfiles: ModelCheckSupportedOption[]
  defaultModel: 'gpt-5.5'
  defaultProfile: ModelCheckProfile
  trustedComparison: ModelCheckTrustedComparisonStatus
}

export interface ModelCheckRunRequest {
  targetType: ModelCheckTargetType
  targetId: string
  model: 'gpt-5.5' | 'gpt-5.4'
  profile?: ModelCheckProfile
  trustedComparison?: boolean
  trustedComparisonAccountId?: string
}

export interface ModelCheckItemSummary {
  id: string
  runId: string
  itemKey: string
  itemType: string
  status: ModelCheckItemStatus
  score: number
  maxScore: number
  durationMs?: number
  traceId?: string
  evidenceSummary: Record<string, unknown>
  errorCode?: string
  errorMessage?: string
  createdAt: string
  updatedAt: string
}

export interface ModelCheckRunSummary {
  id: string
  systemAccountId?: string
  actorSystemAccountId?: string
  providerCode: ProviderCode
  targetType: ModelCheckTargetType
  targetId: string
  targetName?: string
  targetOwnerSystemAccountId?: string
  accountId?: string
  groupId?: string
  apiKeyId?: string
  model: 'gpt-5.5' | 'gpt-5.4'
  profile: ModelCheckProfile
  trustedComparison: boolean
  trustedComparisonAvailable: boolean
  level: ModelCheckLevel
  score: number
  maxScore: number
  status: ModelCheckRunStatus
  message: string
  traceId?: string
  probeSetVersion: string
  startedAt: string
  finishedAt?: string
  durationMs?: number
  requestSummary: Record<string, unknown>
  resultSummary: Record<string, unknown>
  errorCode?: string
  errorMessage?: string
  createdAt: string
  updatedAt: string
}

export interface ModelCheckRunDetail extends ModelCheckRunSummary {
  checks: ModelCheckItemSummary[]
}

export interface ModelCheckRunListResult {
  items: ModelCheckRunSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
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
  authorizationSources?: ResourceAuthorizationSourceSummary[]
  permissions?: ResourcePermissions
}

export interface GroupListResult {
  items: GroupSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
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

export interface ResourceAuthorizationUsageDetail {
  systemAccountId: string
  systemAccountName?: string
  username?: string
  requestCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  totalTokens: number
  totalCost: number
  lastUsedAt?: string
  rangeUsage?: AccountUsageSummary
}

export interface ResourceAuthorizationSummary {
  id: string
  resourceType: ResourceAuthorizationResourceType
  resourceId: string
  resourceName?: string
  resourceOwnerSystemAccountId: string
  resourceOwnerSystemAccountName?: string
  granteeType?: ResourceAuthorizationGranteeType
  granteeSystemAccountId?: string
  granteeSystemAccountName?: string
  granteeUsername?: string
  granteeTeamId?: string
  granteeTeamName?: string
  scope: 'use'
  status: AuthorizationStatus
  remark?: string
  expiresAt?: string
  limits?: RequestQuotaLimits
  resourceAccountExpiresAt?: string
  effectiveSourceType?: ResourceAuthorizationSourceType
  effectiveSourceTeamId?: string
  effectiveSourceTeamName?: string
  activatedAt?: string
  lastSourceChangedAt?: string
  authorizationSources: ResourceAuthorizationSourceSummary[]
  usage: AccountUsageSummary
  lastUsedAt?: string
  usageBySystemAccount?: ResourceAuthorizationUsageDetail[]
  usageBySystemAccountTotal?: number
  usageBySystemAccountPage?: number
  usageBySystemAccountPageSize?: number
  usageBySystemAccountHasMore?: boolean
  usageRange?: AccountUsageStatsRange
  permissions?: Pick<ResourcePermissions, 'canEdit' | 'canAuthorize'>
  createdBy: string
  createdAt: string
  revokedBy?: string
  revokedAt?: string
  revokedReason?: string
  updatedAt: string
}

export interface ResourceAuthorizationListResult {
  items: ResourceAuthorizationSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export type ResourceAuthorizationUsageSummary = ResourceAuthorizationSummary

export interface AuthorizationTeamUsageRow {
  id: string
  teamId: string
  teamName: string
  status: SystemTeamStatus
  resourceType?: ResourceAuthorizationResourceType
  resourceId?: string
  resourceName?: string
  accountId?: string
  accountName?: string
  accountOwnerSystemAccountId?: string
  accountOwnerSystemAccountName?: string
  usage: AccountUsageSummary
  lastUsedAt?: string
}

export interface AuthorizationTeamUsageOverview {
  range: AccountUsageStatsRange
  summary: AccountUsageSummary
  rows: AuthorizationTeamUsageRow[]
  teamCount: number
  total: number
  page: number
  pageSize: number
  hasMore: boolean
}

export interface AuthorizationUserUsageRow {
  id: string
  systemAccountId: string
  userName: string
  username?: string
  teamNames?: string[]
  resourceType?: ResourceAuthorizationResourceType
  resourceId?: string
  resourceName?: string
  accountId?: string
  accountName?: string
  accountOwnerSystemAccountId?: string
  accountOwnerSystemAccountName?: string
  sourceLabels: string[]
  usage: AccountUsageSummary
  lastUsedAt?: string
}

export interface AuthorizationUserUsageOverview {
  range: AccountUsageStatsRange
  summary: AccountUsageSummary
  rows: AuthorizationUserUsageRow[]
  userCount: number
  total: number
  page: number
  pageSize: number
  hasMore: boolean
}

export type ApiKeyGroupBindingStatus = 'active' | 'disabled'
export type ApiKeyGroupRouteStrategy = 'priority_failover' | 'round_robin' | 'weighted_round_robin'
export type ApiKeyAvailabilityScheduleMode = 'allow_windows'
export type ApiKeyAvailabilityScheduleExceptionAction = 'allow' | 'deny'

export interface ApiKeyGroupBindingSummary {
  id: string
  groupId: string
  groupName?: string
  providerCode?: ProviderCode
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  priority: number
  weight: number
  status: ApiKeyGroupBindingStatus
  groupEnabled: boolean
}

export interface ApiKeyAvailabilityScheduleWindow {
  daysOfWeek: number[]
  start: string
  end: string
}

export type ApiKeyAvailabilityScheduleException =
  | {
    date: string
    action: 'allow'
    windows: Array<Pick<ApiKeyAvailabilityScheduleWindow, 'start' | 'end'>>
  }
  | {
    date: string
    action: 'deny'
    windows?: never
  }

export interface ApiKeyAvailabilitySchedule {
  enabled: boolean
  timezone: string
  mode: ApiKeyAvailabilityScheduleMode
  windows: ApiKeyAvailabilityScheduleWindow[]
  dateRange?: {
    startDate?: string
    endDate?: string
  }
  exceptions?: ApiKeyAvailabilityScheduleException[]
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

export interface ApiKeySummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  name: string
  description?: string
  keyPrefix: string
  keySuffix: string
  key: string
  status: 'active' | 'disabled'
  groupRouteStrategy: ApiKeyGroupRouteStrategy
  groupBindings: ApiKeyGroupBindingSummary[]
  groupOwnerSystemAccountName?: string
  expiresAt?: string
  quotaLimits: ApiKeyQuotaLimits
  availabilitySchedule?: ApiKeyAvailabilitySchedule
  availabilityScheduleActive?: boolean
  usage: AccountUsageSummary
}

export interface RequestQuotaLimit {
  enabled: boolean
  /** USD cost quota. */
  limit: number
}

export interface RequestHourlyQuotaLimit extends RequestQuotaLimit {
  hours: number
}

export interface RequestQuotaLimits {
  hourly?: RequestHourlyQuotaLimit
  daily?: RequestQuotaLimit
  weekly?: RequestQuotaLimit
  monthly?: RequestQuotaLimit
  total?: RequestQuotaLimit
}

export type ApiKeyQuotaLimit = RequestQuotaLimit
export type ApiKeyHourlyQuotaLimit = RequestHourlyQuotaLimit
export type ApiKeyQuotaLimits = RequestQuotaLimits
