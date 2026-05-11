export type ProviderCode = string
export type AccountType = string
export type AccountStatus = 'active' | 'disabled' | 'error' | 'rate_limited' | 'temporary_unavailable'
export type AccountTrafficMigrationSourceStatus = 'temporary_unavailable' | 'disabled'
export type SystemAccountRole = 'admin' | 'user'
export type SystemAccountStatus = 'active' | 'disabled'
export type ResourceAccessType = 'owner' | 'authorized'
export type AccountUsageAccessType = 'owner' | 'authorized' | 'account_authorized' | 'group_authorized'
export type GroupUsageAccessType = 'owner' | 'authorized'
export type AuthorizationStatus = 'active' | 'paused' | 'expired' | 'revoked'
export type SystemTeamStatus = 'active' | 'disabled'
export type SystemTeamMemberStatus = 'active' | 'removed'
export type ResourceAuthorizationResourceType = 'account' | 'group'
export type ResourceAuthorizationSourceType = 'manual' | 'team'
export type ResourceAuthorizationSourceStatus = 'active' | 'superseded' | 'revoked'
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
  description?: string
  enabled: boolean
  baseUrl: string
  accountTypes: AccountType[]
  capabilities: string[]
}

export interface ProviderModelPricing {
  providerCode: ProviderCode
  model: string
  mode?: string
  releaseDate?: string
  shutdownDate?: string
  supportedApiProtocols: Array<'chat_completions' | 'responses' | 'completions' | 'images' | 'audio' | 'realtime'>
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
  imageInputUsdPer1M?: number
  imageOutputUsdPer1M?: number
  outputUsdPerImage?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  maxTokens?: number
  supportsPromptCaching: boolean
  supportsServiceTier: boolean
  source: string
}

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

export interface AccountUsageSummary {
  requestCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  totalTokens: number
  totalCost: number
  lastUsedAt?: string
}

export type UsageOverviewWindowKey = 'last1d' | 'last3d' | 'last7d' | 'last30d'

export interface UsageOverviewWindowDefinition {
  key: UsageOverviewWindowKey
  label: string
  hours: number
}

export type AiPerformanceWindowKey = 'last1d' | 'last3d' | 'last7d'

export interface AiPerformanceWindowDefinition {
  key: AiPerformanceWindowKey
  label: string
  hours: number
}

export interface AiPerformanceAccount {
  id: string
  name: string
  status: AccountStatus
  providerCode: ProviderCode
  systemAccountId: string
  systemAccountName?: string
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
  requestCountLast7d: number
}

export interface AiPerformancePoint {
  statHour: string
  requestCount: number
  firstTokenCount: number
  averageFirstTokenMs?: number
  durationCount: number
  averageDurationMs?: number
}

export interface AiPerformanceAccountSeries {
  accountId: string
  accountName: string
  systemAccountId: string
  points: AiPerformancePoint[]
}

export interface AiPerformanceOverview {
  window: AiPerformanceWindowDefinition
  defaultAccounts: AiPerformanceAccount[]
  selectedAccounts: AiPerformanceAccount[]
  accounts: AiPerformanceAccount[]
  hourlySeries: AiPerformanceAccountSeries[]
  summary: {
    requestCount: number
    firstTokenCount: number
    averageFirstTokenMs?: number
    durationCount: number
    averageDurationMs?: number
  }
  statsLagSeconds: number
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

export interface GroupAccountStats {
  total: number
  available: number
  active: number
  disabled: number
  error: number
  rateLimited: number
  currentConcurrency: number
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
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
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
  accountExpiresAt?: string
  cooldownUntil?: string
  lastErrorMessage?: string
  localStatus?: AccountStatus
  localCooldownUntil?: string
  localLastErrorMessage?: string
  lastUsedAt?: string
  todayUsage: AccountUsageSummary
  usage: AccountUsageSummary
  oauthUsage?: AccountOAuthUsageSnapshot
  accessType?: ResourceAccessType
  accountAuthorizationId?: string
  boundGroupId?: string
  boundGroupName?: string
  groupBindStatus?: AccountGroupBindStatus
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  authorizationStatus?: AuthorizationStatus
  authorizationSources?: ResourceAuthorizationSourceSummary[]
  permissions?: ResourcePermissions
  authorizationUsageAvailable?: boolean
  authorizationCount?: number
  authorizationTeamCount?: number
}

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
  total: number
  page: number
  pageSize: number
  statsLagSeconds: number
}

export interface AccountTestResult {
  accountId: string
  accountName: string
  providerCode: ProviderCode
  type: AccountType
  success: boolean
  statusCode?: number
  message: string
  model?: string
  requestUrl?: string
  requestBody?: unknown
  responseHeaders?: Record<string, string | string[]>
  responseBody?: unknown
  responseText?: string
  outputText?: string
  modelsUrl?: string
  proxyUrl?: string
  tokenRefreshed?: boolean
  durationMs?: number
  firstTokenMs?: number
  accountStatusChanged?: boolean
  accountStatus?: AccountStatus
  errorPolicyAction?: 'none' | 'retry_next' | 'cooldown' | 'disable' | 'default_cooldown'
  errorPolicyReason?: string
}

export interface ErrorPolicySummary {
  id: string
  name: string
  enabled: boolean
  rules: Array<Record<string, unknown>>
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
  accountIds: string[]
  accountStats: GroupAccountStats
  accessType?: ResourceAccessType
  groupAuthorizationId?: string
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  authorizationStatus?: AuthorizationStatus
  authorizationSources?: ResourceAuthorizationSourceSummary[]
  permissions?: ResourcePermissions
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
  dailyUsage?: AccountUsageDailyPoint[]
  usageBuckets?: ResourceAuthorizationUsageBucket[]
}

export type ResourceAuthorizationUsageGroupBy = 'day' | 'week'

export interface ResourceAuthorizationUsageBucket extends AccountUsageSummary {
  bucketKey: string
  startDate: string
  endDate: string
}

export interface ResourceAuthorizationSummary {
  id: string
  resourceType: ResourceAuthorizationResourceType
  resourceId: string
  resourceName?: string
  resourceOwnerSystemAccountId: string
  resourceOwnerSystemAccountName?: string
  granteeSystemAccountId: string
  granteeSystemAccountName?: string
  granteeUsername?: string
  scope: 'use'
  status: AuthorizationStatus
  remark?: string
  expiresAt?: string
  limits?: RequestQuotaLimits
  modelPolicy?: Record<string, unknown>
  effectiveSourceType?: ResourceAuthorizationSourceType
  effectiveSourceTeamId?: string
  effectiveSourceTeamName?: string
  activatedAt?: string
  lastSourceChangedAt?: string
  sources: ResourceAuthorizationSourceSummary[]
  authorizationSources?: ResourceAuthorizationSourceSummary[]
  usage: AccountUsageSummary
  usageBySystemAccount?: ResourceAuthorizationUsageDetail[]
  usageRange?: AccountUsageStatsRange
  usageGroupBy?: ResourceAuthorizationUsageGroupBy
  usageBuckets?: ResourceAuthorizationUsageBucket[]
  permissions?: Pick<ResourcePermissions, 'canEdit' | 'canAuthorize'>
  createdBy: string
  createdAt: string
  revokedBy?: string
  revokedAt?: string
  revokedReason?: string
  updatedAt: string
}

export type ResourceAuthorizationUsageSummary = ResourceAuthorizationSummary

export interface ApiKeySummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  name: string
  description?: string
  keyPrefix: string
  key: string
  status: 'active' | 'disabled'
  groupId: string
  groupAuthorizationId?: string
  expiresAt?: string
  quotaLimits: ApiKeyQuotaLimits
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
