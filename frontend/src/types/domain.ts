export type ProviderCode = string
export type AccountType = string
export type AccountStatus = 'active' | 'disabled' | 'error' | 'rate_limited' | 'temporary_unavailable'
export type SystemAccountRole = 'admin' | 'user'
export type SystemAccountStatus = 'active' | 'disabled'
export type ResourceAccessType = 'owner' | 'authorized'
export type AuthorizationStatus = 'active' | 'paused' | 'expired' | 'revoked'
export type AuthorizationResourceType = 'account' | 'group'
export type AuthorizationSourceType = 'manual' | 'team'
export type AuthorizationSourceStatus = 'active' | 'superseded' | 'revoked'
export type TeamStatus = 'active' | 'disabled'
export type TeamMemberStatus = 'active' | 'removed'

export interface CurrentUserSummary {
  id: string
  username: string
  displayName: string
  role: SystemAccountRole
  mustChangePassword: boolean
}

export interface CaptchaChallengeSummary {
  captchaId: string
  image: string
  expiresAt: string
}

export interface SystemAccountSummary {
  id: string
  username: string
  displayName: string
  role: SystemAccountRole
  status: SystemAccountStatus
  mustChangePassword: boolean
  lastLoginAt?: string
  createdAt: string
  updatedAt: string
}

export interface ProviderDefinition {
  id: string
  code: ProviderCode
  name: string
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
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
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
  clientCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  totalTokens: number
  totalCost: number
  lastUsedAt?: string
}

export type UsageStatsWindowKey = 'last1d' | 'last3d' | 'last7d' | 'last15d' | 'last30d' | 'total'

export interface UsageStatsWindowDefinition {
  key: UsageStatsWindowKey
  label: string
  days?: number
}

export type UsageByWindow = Record<UsageStatsWindowKey, AccountUsageSummary>

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
  proxyProfileId?: string
  passthroughEnabled: boolean
  errorPolicyId?: string
  schedulable: boolean
  accountExpiresAt?: string
  cooldownUntil?: string
  lastErrorMessage?: string
  lastUsedAt?: string
  todayUsage: AccountUsageSummary
  usage: AccountUsageSummary
  oauthUsage?: AccountOAuthUsageSnapshot
  accessType?: ResourceAccessType
  accountAuthorizationId?: string
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  authorizationStatus?: AuthorizationStatus
  permissions?: ResourcePermissions
  authorizationUsageAvailable?: boolean
  authorizationCount?: number
  authorizationTeamCount?: number
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
  permissions?: ResourcePermissions
}

export interface SystemTeamMemberSummary {
  id: string
  teamId: string
  systemAccountId: string
  systemAccountName?: string
  systemAccountUsername?: string
  username?: string
  memberRole: 'member'
  status: TeamMemberStatus
  joinedAt?: string
  removedAt?: string
  createdAt: string
  updatedAt: string
}

export interface SystemTeamSummary {
  id: string
  name: string
  description?: string
  status: TeamStatus
  createdBy: string
  createdAt: string
  updatedAt: string
  memberCount?: number
  members?: SystemTeamMemberSummary[]
}

export interface AuthorizationSourceSummary {
  id: string
  authorizationId?: string
  sourceType: AuthorizationSourceType
  sourceTeamId?: string
  sourceTeamName?: string
  status: AuthorizationSourceStatus
  activatedAt?: string
  endedAt?: string
  endedReason?: string
  createdBy?: string
  createdAt: string
  revokedBy?: string
  revokedAt?: string
  updatedAt?: string
}

export interface AuthorizationUserUsageDetail {
  systemAccountId: string
  systemAccountName?: string
  username?: string
  requestCount: number
  clientCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  totalTokens: number
  totalCost: number
  lastUsedAt?: string
}

export interface ResourceAuthorizationSummary {
  id: string
  resourceType: AuthorizationResourceType
  resourceId: string
  resourceName?: string
  resourceOwnerSystemAccountId: string
  resourceOwnerSystemAccountName?: string
  granteeSystemAccountId: string
  granteeSystemAccountName?: string
  granteeUsername?: string
  status: AuthorizationStatus
  scope: 'use'
  remark?: string
  expiresAt?: string
  limits?: Record<string, unknown>
  modelPolicy?: Record<string, unknown>
  effectiveSourceType?: AuthorizationSourceType
  effectiveSourceTeamId?: string
  effectiveSourceTeamName?: string
  activatedAt?: string
  lastSourceChangedAt?: string
  createdAt: string
  updatedAt: string
  revokedAt?: string
  revokedReason?: string
  createdBy?: string
  revokedBy?: string
  sources?: AuthorizationSourceSummary[]
  authorizationSources: AuthorizationSourceSummary[]
  usage: AccountUsageSummary
  usageBySystemAccount?: AuthorizationUserUsageDetail[]
  usageByWindow?: UsageByWindow
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
  usageByWindow: UsageByWindow
  authorizationUsageAvailable: boolean
  authorizationCount: number
  authorizationTeamCount: number
}

export interface AccountUsageStatsOverview {
  windows: UsageStatsWindowDefinition[]
  rows: AccountUsageStatsRow[]
  statsLagSeconds: number
}

export interface AuthorizationTeamMemberUsageDetail {
  authorizationId: string
  systemAccountId: string
  systemAccountName?: string
  username?: string
  usageByWindow: UsageByWindow
}

export interface AuthorizationTeamUsageDetail {
  teamId: string
  teamName?: string
  usageByWindow: UsageByWindow
  memberUsage: AuthorizationTeamMemberUsageDetail[]
}

export interface AccountAuthorizationUsageOverview {
  resourceType: 'account'
  resourceId: string
  resourceName: string
  resourceOwnerSystemAccountId: string
  resourceOwnerSystemAccountName?: string
  windows: UsageStatsWindowDefinition[]
  users: Array<ResourceAuthorizationSummary & { usageByWindow: UsageByWindow }>
  teams: AuthorizationTeamUsageDetail[]
  statsLagSeconds: number
}

export interface ApiKeySummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  name: string
  keyPrefix: string
  key: string
  status: 'active' | 'disabled'
  groupId: string
  expiresAt?: string
}

export interface CreatedApiKey extends ApiKeySummary {}


export interface OpenAIAuthURLResult {
  authUrl: string
  sessionId: string
}

export interface ProxyProfileSummary {
  id: string
  name: string
  type: 'http' | 'https' | 'socks5' | string
  host: string
  port: number
  username?: string
  enabled: boolean
  testStatus: string
  lastTestedAt?: string
}

export interface UsageRecordLogSnapshot {
  [key: string]: unknown
}

export interface UsageRecordCostBreakdown {
  inputCostUsd?: number
  outputCostUsd?: number
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cacheReadCostUsd?: number
  accountChargeUsd?: number
  multiplier: 1
}

export interface UsageRecordSummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  requestId: string
  clientIp?: string
  apiKeyId?: string
  apiKeyName?: string
  groupId?: string
  groupName?: string
  accountId?: string
  accountName?: string
  endpoint?: string
  providerCode?: string
  model?: string
  stream: boolean
  statusCode?: number
  success: boolean
  firstTokenMs?: number
  durationMs?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  costUsd?: number
  costBreakdown?: UsageRecordCostBreakdown
  errorCode?: string
  errorMessage?: string
  requestSnapshot?: UsageRecordLogSnapshot
  responseSnapshot?: UsageRecordLogSnapshot
  createdAt: string
}

export interface UsageStatsOverview {
  today: AccountUsageSummary & {
    successCount: number
    errorCount: number
    errorRate: number
    averageDurationMs?: number
    averageFirstTokenMs?: number
  }
  totals: AccountUsageSummary & {
    successCount: number
    errorCount: number
    errorRate: number
    averageDurationMs?: number
    averageFirstTokenMs?: number
  }
  hourlyTrend: Array<{
    statHour: string
    requestCount: number
    totalTokens: number
    totalCost: number
    averageDurationMs?: number
    errorCount: number
  }>
  modelDistribution: Array<{
    model: string
    providerCode: string
    requestCount: number
    totalTokens: number
    totalCost: number
  }>
  errors: Array<{
    errorCode: string
    providerCode: string
    statusCode?: number
    errorMessage?: string
    errorCount: number
  }>
  statsLagSeconds: number
}

export interface SystemMetricsOverview {
  latest?: {
    sampledAt: string
    cpuPercent?: number
    memoryUsedPercent?: number
    memoryTotalBytes?: number
    memoryFreeBytes?: number
    processRssBytes?: number
    processHeapUsedBytes?: number
    processHeapTotalBytes?: number
    eventLoopLagMs?: number
    networkRxBytesPerSecond?: number
    networkTxBytesPerSecond?: number
    networkRxTotalBytes?: number
    networkTxTotalBytes?: number
    dbFileBytes?: number
    statsLagSeconds?: number
  }
  hourlyTrend: Array<{
    statHour: string
    sampleCount: number
    cpuPercentAvg?: number
    cpuPercentMax?: number
    memoryUsedPercentAvg?: number
    memoryUsedPercentMax?: number
    eventLoopLagMsAvg?: number
    eventLoopLagMsMax?: number
    networkRxBytesPerSecondAvg?: number
    networkRxBytesPerSecondMax?: number
    networkTxBytesPerSecondAvg?: number
    networkTxBytesPerSecondMax?: number
    networkRxTotalBytesMax?: number
    networkTxTotalBytesMax?: number
    processRssBytesMax?: number
    processHeapUsedBytesMax?: number
    dbFileBytesMax?: number
    statsLagSecondsMax?: number
  }>
}

export interface SystemSettings {
  appName?: string
  appIcon?: string
  defaultTemporaryUnschedulableMinutes?: number
  temporaryUnschedulableRetryIntervalSeconds?: number
  temporaryUnschedulableRetryAttempts?: number
  streamCircuitBreakerEnabled?: boolean
  streamRequestTimeoutSeconds?: number
  streamIdleTimeoutSeconds?: number
  streamFailureThresholdCount?: number
  streamFailureThresholdWindowMinutes?: number
  [key: string]: unknown
}

export interface GlobalSettings {
  appName?: string
  appIcon?: string
  [key: string]: unknown
}

