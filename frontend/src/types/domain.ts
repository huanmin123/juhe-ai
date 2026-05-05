export type ProviderCode = string
export type AccountType = string
export type AccountStatus = 'active' | 'disabled' | 'error' | 'rate_limited' | 'temporary_unavailable'
export type SystemAccountRole = 'admin' | 'user'
export type SystemAccountStatus = 'active' | 'disabled'
export type ResourceAccessType = 'owner' | 'authorized'
export type AuthorizationStatus = 'active' | 'paused' | 'expired' | 'revoked'
export type AccountGroupBindStatus = 'bound' | 'authorization_unavailable'
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
  description?: string
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
  boundGroupId?: string
  boundGroupName?: string
  groupBindStatus?: AccountGroupBindStatus
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
  description?: string
  keyPrefix: string
  key: string
  status: 'active' | 'disabled'
  groupId: string
  groupAuthorizationId?: string
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
  description?: string
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
  traceId: string
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

export type AuditOutcome = 'success' | 'success_after_retry' | 'gateway_failed' | 'upstream_failed' | 'stream_failed' | 'client_aborted'
export type AuditPayloadPartType = 'client_request' | 'upstream_request' | 'upstream_response' | 'gateway_response' | 'gateway_error'

export interface AuditLogSummary {
  id: string
  traceId: string
  systemAccountId?: string
  systemAccountName?: string
  apiKeyId?: string
  apiKeyName?: string
  groupId?: string
  groupName?: string
  accountId?: string
  accountName?: string
  providerCode?: string
  method: string
  path: string
  queryString?: string
  model?: string
  stream: boolean
  clientIp?: string
  userAgent?: string
  auditOutcome: AuditOutcome
  success: boolean
  finalStatusCode?: number
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
  sampleBucket: number
  sampleReason: string
  attemptCount: number
  payloadCount: number
  payloadBytes: number
  captureStatus: string
  startedAt: string
  endedAt: string
  durationMs?: number
  firstTokenMs?: number
  createdAt: string
}

export interface AuditLogAttemptSummary {
  id: string
  attemptIndex: number
  accountId?: string
  accountName?: string
  accountOwnerSystemAccountId?: string
  groupId?: string
  groupName?: string
  proxyUrl?: string
  providerCode?: string
  upstreamMethod: string
  upstreamUrl: string
  upstreamStatusCode?: number
  success: boolean
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
  startedAt: string
  endedAt?: string
  durationMs?: number
}

export interface AuditLogPayloadSummary {
  id: string
  attemptId?: string
  partType: AuditPayloadPartType
  sequenceIndex: number
  contentType?: string
  contentEncoding?: string
  bodySha256?: string
  sizeBytes: number
  createdAt: string
  hasHeaders: boolean
  hasBody: boolean
}

export interface AuditLogDetail extends AuditLogSummary {
  attempts: AuditLogAttemptSummary[]
  payloads: AuditLogPayloadSummary[]
}

export interface AuditLogPayloadDetail extends AuditLogPayloadSummary {
  headers?: Record<string, string | string[]>
  bodyText?: string
  bodyBase64?: string
}

export interface AuditLogRuntime {
  queueLength: number
  queueBytes: number
  flushLastSuccessAt?: string
  flushLastError?: string
  droppedSuccessCount: number
  droppedFailureCount: number
  droppedOverflowCount: number
  droppedOversizeCount: number
  activeCaptureCount: number
  settings: {
    enabled: boolean
    successSampleRate: number
    flushIntervalSeconds: number
    batchSize: number
    queueMaxItems: number
    queueMaxBytes: number
    activeCaptureMaxBytes: number
    retentionDays: number
  }
}

export interface AuditLogListResult {
  items: AuditLogSummary[]
  total: number
  page: number
  pageSize: number
}

export type RuntimeLogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal'

export interface RuntimeLogSummary {
  id: string
  time: string
  level: RuntimeLogLevel | string
  traceId?: string
  event?: string
  message?: string
  errorMessage?: string
  rawJson: string
  createdAt: string
}

export interface RuntimeLogSearchResult {
  items: RuntimeLogSummary[]
  total: number
  page: number
  pageSize: number
  elapsedMs: number
  retentionDays: number
}

export interface RuntimeLogGrepItem {
  id: string
  file: string
  fileName: string
  lineNumber?: number
  lineNumberFromEnd: number
  time: string
  level: RuntimeLogLevel | string
  traceId?: string
  event?: string
  message?: string
  errorMessage?: string
  rawJson: string
  line: string
}

export interface RuntimeLogGrepResult {
  available: boolean
  mode?: 'rg'
  elapsedMs: number
  keywords: string[]
  items: RuntimeLogGrepItem[]
  limit: number
  truncated: boolean
  scannedFileCount: number
  message?: string
  installSteps?: string[]
}

export interface RuntimeLogIndexRuntime {
  queueLength: number
  droppedCount: number
  flushLastSuccessAt?: string
  flushLastError?: string
  retentionDays: number
}

export interface RuntimeLogFacets {
  retentionDays: number
  earliestIndexedAt?: string
  latestIndexedAt?: string
  totalIndexed: number
  levels: Array<{ value: string; count: number }>
  events: string[]
  runtime: RuntimeLogIndexRuntime
}

export type UsageOverviewWindowKey = 'last1d' | 'last3d' | 'last7d' | 'last30d'

export interface UsageOverviewWindowDefinition {
  key: UsageOverviewWindowKey
  label: string
  hours: number
}

export interface UsageStatsOverview {
  window: UsageOverviewWindowDefinition
  summary: AccountUsageSummary & {
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
  auditLogEnabled?: boolean
  auditLogSuccessSampleRate?: number
  auditLogFlushIntervalSeconds?: number
  auditLogBatchSize?: number
  auditLogQueueMaxItems?: number
  auditLogQueueMaxBytesMb?: number
  auditLogActiveCaptureMaxBytesMb?: number
  auditLogRetentionDays?: number
  [key: string]: unknown
}

export interface GlobalSettings {
  appName?: string
  appIcon?: string
  [key: string]: unknown
}

