import type { AccountGroupBindStatus, AccountStatus, AccountTrafficMigrationSourceStatus, AccountType, AuthorizationStatus, GroupType, ProviderCode, ResourceAccessType } from './base'
import type { RequestQuotaLimits } from './access'
import type { AuthorizationSourceSummary } from './authorizations'
import type { AccountUsageSummary } from './usage-stats'

export type AccountClientCompatibility = 'openai_standard' | 'codex_responses' | 'anthropic_native' | 'claude_code'
export type AccountGptServiceTierOverride = string
export type AccountGptReasoningEffortOverride = string
export type AccountSupportedEndpointMode =
  | 'images_json'
  | 'chat_json'
  | 'chat_sse'
  | 'responses_json'
  | 'responses_sse'
  | 'messages_json'
  | 'messages_sse'
  | 'message_token_counting'
  | 'generate_content_json'
  | 'generate_content_sse'
  | 'count_tokens'
  | 'embed_content'
  | 'interactions_json'
  | 'interactions_sse'
export type AccountHealthCheckEndpointMode = Extract<
  AccountSupportedEndpointMode,
  | 'chat_json'
  | 'chat_sse'
  | 'responses_json'
  | 'responses_sse'
  | 'messages_json'
  | 'messages_sse'
  | 'generate_content_json'
  | 'generate_content_sse'
  | 'interactions_json'
  | 'interactions_sse'
>
export type AccountApiKeyRuntimeStatus = 'active' | 'temporary_unavailable' | 'rate_limited' | 'error' | 'disabled'

export interface AccountCredentials {
  api_key?: string
  api_keys?: string[]
  api_key_strategy?: 'round_robin' | 'weighted_round_robin'
  api_key_weights?: number[]
  base_url?: string
  supported_endpoint_modes?: AccountSupportedEndpointMode[]
  access_token?: string
  refresh_token?: string
  client_id?: string
  id_token?: string
  email?: string
  expires_at?: string
  account_id?: string
  chatgpt_user_id?: string
  plan_type?: string
  service_tier_override?: Exclude<AccountGptServiceTierOverride, ''>
  reasoning_effort_override?: Exclude<AccountGptReasoningEffortOverride, ''>
  response_inspection_rules?: unknown[]
  codex_responses_safe_repair_enabled?: boolean
  codex_responses_strict_intercept_enabled?: boolean
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

export type AccountRuntimeAvailabilityStatus = 'normal' | 'degraded' | 'local_suppressed' | 'half_open' | 'precheck_pending' | 'precheck_failed'

export interface AccountRuntimeAvailability {
  status: AccountRuntimeAvailabilityStatus
  reason?: string
  since?: string
  probePresentation?: AccountRuntimeProbePresentation
}

export interface AccountRuntimeProbePresentation {
  lastObservation?: AccountProbeObservation
  schedule: {
    state: 'scheduled' | 'due_waiting' | 'running' | 'none'
    nextAttemptAt?: string
  }
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
  | 'instance_expired'
  | 'instance_pending_test'
  | 'instance_disabled'
  | 'instance_error'
  | 'instance_rate_limited'
  | 'instance_temporary_unavailable'
  | 'instance_cooldown'
  | 'instance_unschedulable'
  | 'binding_missing'
  | 'api_key_pool_unavailable'
  | 'runtime_degraded'
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
  | 'api_key_pool'
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

export interface AccountProbeObservation {
  observationId: string
  attemptedAt: string
  result: 'success' | 'failed'
  httpStatus?: number
  errorCode?: string
  reason?: string
  traceId?: string
}

export interface AccountProbeSummary {
  kind: 'health_check' | 'activation_check' | 'cooldown_retest' | 'runtime_probe' | 'api_key_retest' | 'source_account_probe'
  lastObservation?: AccountProbeObservation
  schedule: {
    state: 'scheduled' | 'due_waiting' | 'running' | 'none'
    nextAttemptAt?: string
  }
}

export interface AccountAvailabilityPresentation {
  status: string
  label: string
  reason?: string
  action?: string
  statusBoundary?: {
    at: string
    kind: 'policy_ttl_expiry' | 'quota_reset' | 'cooldown_expiry' | 'account_expired' | 'authorization_expired' | 'source_expired'
  }
  probe?: AccountProbeSummary
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
  currentConcurrency?: number
  currentConcurrencyAvailable?: boolean
  concurrencyLimit: number
  todayUsage?: AccountUsageSummary
  usage: AccountUsageSummary
}

export interface AccountModelMapping {
  sourceModel: string
  sourceEndpointFamily: 'chat_completions' | 'responses' | 'messages' | 'generate_content' | 'stream_generate_content'
  upstreamModel: string
  upstreamEndpointFamily: 'chat_completions' | 'responses' | 'messages' | 'generate_content'
  enabled: boolean
}

export interface AccountTagSummary {
  id: string
  name: string
  accountCount?: number
  createdAt?: string
  updatedAt?: string
}

export type AccountBalanceBuiltinAdapter = 'sub2api' | 'newapi' | 'litellm' | 'user_balance'
export type AccountBalanceAdapter = 'builtin' | 'custom'
export type AccountBalanceStatus = 'pending' | 'refreshing' | 'fresh' | 'unlimited' | 'unsupported' | 'failed'

export interface AccountBalanceQueryConfig {
  adapter: AccountBalanceAdapter
  intervalMinutes: number
  preferredBuiltinAdapter?: AccountBalanceBuiltinAdapter
  custom?: {
    path: string
    remainingPointer?: string
    totalPointer?: string
    usedPointer?: string
    divisor?: string
  }
}

export interface AccountBalanceSnapshot {
  status: AccountBalanceStatus
  remainingUsd?: string
  errorMessage?: string
  lastAttemptAt?: string
  lastSuccessAt?: string
  consecutiveTransientFailures?: number
  lastTransientErrorMessage?: string
  lastTransientFailureAt?: string
}

export interface AccountTagsUpdateResult {
  id: string
  tags: Array<Pick<AccountTagSummary, 'id' | 'name'>>
}

export interface AccountSummary {
  id: string
  configRevision?: number
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
  accountRuntimeAvailabilityAvailable?: boolean
  runtimeAvailability?: AccountRuntimeAvailability
  effectiveAvailability?: AccountEffectiveAvailability
  availabilityPresentation?: AccountAvailabilityPresentation
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  clientCompatibility: AccountClientCompatibility
  supportedModels?: string[]
  modelMappings?: AccountModelMapping[]
  tags?: AccountTagSummary[]
  healthCheckModel: string
  healthCheckEndpointMode: AccountHealthCheckEndpointMode
  qualityScore?: number
  qualityState?: string
  qualityEwmaFirstTokenMs?: number
  qualityRecentAvgFirstTokenMs?: number
  qualityRecentRequestCount?: number
  qualityRecentErrorCount?: number
  qualityRecentSuccessRate?: number
  qualityLastErrorAt?: string
  qualityLastErrorMessage?: string
  qualityUpdatedAt?: string
  proxyProfileId?: string
  proxyProfileName?: string
  proxyProfileType?: 'http' | 'https' | 'socks5' | 'socks5h'
  proxyProfileEnabled?: boolean
  proxyProfileUnavailable?: boolean
  proxyProfileErrorMessage?: string
  schedulable: boolean
  availabilitySchedule?: AccountAvailabilitySchedule
  accountExpiresAt?: string
  cooldownUntil?: string
  lastErrorCode?: string
  lastErrorMessage?: string
  lastErrorTraceId?: string
  cooldownRetestFailureCount?: number
  cooldownRetestObservationStartedAt?: string
  cooldownRetestLastAt?: string
  cooldownRetestLastStatusCode?: number
  temporaryUnavailableContinuousProbeEnabled?: boolean
  lastHealthCheckAt?: string
  nextHealthCheckAt?: string
  lastHealthSuccessAt?: string
  healthCheckFailureCount?: number
  healthCheckFailureStartedAt?: string
  lastHealthCheckStatusCode?: number
  lastHealthCheckErrorCode?: string
  lastHealthCheckErrorMessage?: string
  lastHealthCheckTraceId?: string
  apiKeyRuntime?: AccountApiKeyRuntimeSummary
  apiKeyRuntimeDetails?: AccountApiKeyRuntimeDetail[]
  streamFailureCount?: number
  streamFailureWindowStartedAt?: string
  balanceQueryEnabled?: boolean
  balanceQueryConfig?: AccountBalanceQueryConfig
  balanceQueryNextRefreshAt?: string
  balanceSnapshot?: AccountBalanceSnapshot
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
  // List/options endpoints may return only the permissions relevant to that action.
  permissions?: Partial<ResourcePermissions>
  authorizationUsageAvailable?: boolean
  authorizationCount?: number
  authorizationTeamCount?: number
}

export type AccountListItem = Omit<AccountSummary,
  | 'credentials'
  | 'supportedModels'
  | 'modelMappings'
  | 'apiKeyRuntimeDetails'
  | 'apiKeyRuntime'
  | 'balanceQueryEnabled'
  | 'balanceQueryConfig'
  | 'balanceQueryNextRefreshAt'
  | 'balanceSnapshot'
  | 'usage'
  | 'todayUsage'
  | 'currentConcurrency'
  | 'lastUsedAt'
  | 'runtimeAvailability'
  | 'effectiveAvailability'
  | 'availabilityPresentation'
  | 'oauthUsage'
  | 'authorizationSources'
  | 'authorizationUsageAvailable'
  | 'authorizationCount'
  | 'authorizationTeamCount'
>

export interface AccountStatusSnapshotItem {
  id: string
  status: AccountStatus
  schedulable: boolean
  currentConcurrency: number
  cooldownUntil?: string
  lastErrorCode?: string
  lastErrorMessage?: string
  lastErrorTraceId?: string
  cooldownRetestLastAt?: string
  cooldownRetestLastStatusCode?: number
  lastHealthCheckAt?: string
  nextHealthCheckAt?: string
  lastHealthCheckStatusCode?: number
  lastHealthCheckErrorCode?: string
  lastHealthCheckErrorMessage?: string
  lastHealthCheckTraceId?: string
  authorizationStatus?: AuthorizationStatus
  authorizationExpiresAt?: string
  authorizationQuotaExceeded?: boolean
  authorizationInstanceSourceAccountStatus?: AccountStatus
  authorizationInstanceSourceAccountSchedulable?: boolean
  authorizationInstanceSourceAccountExpiresAt?: string
  authorizationInstanceSourceAccountCooldownUntil?: string
  authorizationInstanceSourceAccountLastErrorCode?: string
  authorizationInstanceSourceAccountLastErrorMessage?: string
  authorizationInstanceSourceAccountLastErrorTraceId?: string
  authorizationInstanceSourceAccountCooldownRetestLastAt?: string
  authorizationInstanceSourceAccountCooldownRetestLastStatusCode?: number
  authorizationInstanceSourceAccountLastHealthCheckAt?: string
  authorizationInstanceSourceAccountNextHealthCheckAt?: string
  authorizationInstanceSourceAccountLastHealthCheckStatusCode?: number
  authorizationInstanceSourceAccountLastHealthCheckErrorCode?: string
  authorizationInstanceSourceAccountLastHealthCheckErrorMessage?: string
  authorizationInstanceSourceAccountLastHealthCheckTraceId?: string
  apiKeyRuntime?: AccountApiKeyRuntimeSummary
  balanceQueryEnabled?: boolean
  balanceQueryNextRefreshAt?: string
  balanceSnapshot?: AccountBalanceSnapshot
  runtimeAvailability?: AccountRuntimeAvailability
  effectiveAvailability: AccountEffectiveAvailability
  availabilityPresentation?: AccountAvailabilityPresentation
  lastUsedAt?: string
  todayUsage: AccountUsageSummary
}

export interface AccountStatusSnapshotResult {
  generatedAt: string
  runtimeSnapshot: {
    accountConcurrencyAvailable: boolean
    accountRuntimeAvailabilityAvailable: boolean
  }
  items: AccountStatusSnapshotItem[]
}

export interface AccountBatchEditTarget {
  accountId: string
  configRevision: number
}

export interface AccountBatchEditField<TValue> {
  enabled: boolean
  value: TValue
}

export interface AccountBatchEditUpdates {
  tags?: AccountBatchEditField<string[]>
  proxyProfileId?: AccountBatchEditField<string | null>
  concurrencyLimit?: AccountBatchEditField<number>
  priority?: AccountBatchEditField<number>
  superPriorityEnabled?: AccountBatchEditField<boolean>
  fallbackEnabled?: AccountBatchEditField<boolean>
  accountExpiresAt?: AccountBatchEditField<string | null>
  availabilitySchedule?: AccountBatchEditField<AccountAvailabilitySchedule | null>
  notes?: AccountBatchEditField<string>
  errorHandlingRules?: AccountBatchEditField<unknown[]>
  responseInspectionRules?: AccountBatchEditField<unknown[]>
  supportedModels?: AccountBatchEditField<string[]>
  healthCheckModel?: AccountBatchEditField<string>
  healthCheckEndpointMode?: AccountBatchEditField<AccountHealthCheckEndpointMode>
  modelMappings?: AccountBatchEditField<AccountModelMapping[]>
  supportedEndpointModes?: AccountBatchEditField<AccountSupportedEndpointMode[]>
  serviceTierOverride?: AccountBatchEditField<AccountGptServiceTierOverride | null>
  reasoningEffortOverride?: AccountBatchEditField<AccountGptReasoningEffortOverride | null>
}

export interface AccountBatchEditRequest {
  targets: AccountBatchEditTarget[]
  updates: AccountBatchEditUpdates
}

export interface AccountBatchEditResult {
  batchId: string
  changedFields: string[]
  accounts: AccountSummary[]
}

export interface AccountApiKeyRuntimeSummary {
  total: number
  active: number
  temporaryUnavailable: number
  rateLimited: number
  error: number
  disabled: number
  unavailable: number
  allUnavailable: boolean
  nextProbeAt?: string
}

export interface AccountApiKeyRuntimeDetail {
  keyIndex: number
  keyFingerprintPrefix: string
  keySuffix?: string
  weight: number
  status: AccountApiKeyRuntimeStatus
  failureCount: number
  consecutiveFailures: number
  successCount: number
  cooldownUntil?: string
  nextProbeAt?: string
  lastAttemptAt?: string
  lastSuccessAt?: string
  lastFailureAt?: string
  lastErrorCode?: string
  lastErrorMessage?: string
}

export interface AccountApiKeyRuntimeResponse {
  accountId: string
  configRevision: number
  items: AccountApiKeyRuntimeDetail[]
}

export interface AccountListResult {
  items: AccountListItem[]
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
  upstreamModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
  sourceEndpointFamily?: AccountModelMapping['sourceEndpointFamily']
  upstreamEndpointFamily?: AccountModelMapping['upstreamEndpointFamily']
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
  testEndpointMode?: AccountSupportedEndpointMode
  apiKeyPool?: AccountTestApiKeyPoolResult
}

export interface AccountTestApiKeyPoolResult {
  total: number
  tested: number
  successCount: number
  failedCount: number
  requiredSuccessCount: number
  results: AccountTestApiKeyPoolItemResult[]
}

export interface AccountTestApiKeyPoolItemResult {
  keyIndex: number
  keyPrefix?: string
  keySuffix?: string
  success: boolean
  statusCode?: number
  errorCode?: string
  message: string
  durationMs?: number
}

export type AccountTestTaskStatus = 'queued' | 'running' | 'success' | 'failed' | 'canceled'
export type AccountTestSessionStatus = 'running' | 'canceled' | 'expired' | 'completed'

export interface AccountTestSession {
  id: string
  status: AccountTestSessionStatus
  message?: string
  lastHeartbeatAt: string
  cancelRequestedAt?: string
  finishedAt?: string
  createdAt: string
  updatedAt: string
}

export interface AccountTestSessionDetail {
  session: AccountTestSession
  tasks: AccountTestTask[]
}

export interface AccountTestTask {
  id: string
  sessionId?: string
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
  testEndpointMode?: AccountSupportedEndpointMode
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
  description?: string
  enabled: boolean
  isDefault: boolean
  groupType: GroupType
  schedulingPolicy?: GroupSchedulingPolicy
  accountIds?: string[]
  accountCount?: number
  accountStats: GroupAccountStats
  accessType?: ResourceAccessType
  groupAuthorizationId?: string
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  authorizationStatus?: AuthorizationStatus
  authorizationExpiresAt?: string
  authorizationLimits?: RequestQuotaLimits
  authorizationSources?: AuthorizationSourceSummary[]
  authorizationSourceSummary?: {
    activeSourceCount: number
    hasManual: boolean
    hasTeam: boolean
    teamNames: string[]
  }
  permissions?: Partial<ResourcePermissions>
  canEdit?: boolean
  canDelete?: boolean
  canReturn?: boolean
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

export interface GroupStatusSnapshotResult {
  generatedAt: string
  runtimeSnapshot: {
    accountConcurrencyAvailable: boolean
  }
  items: Array<{
    id: string
    currentConcurrency: number
    todayUsage: AccountUsageSummary
  }>
}

export type GroupOptionSummary = Pick<GroupSummary, 'id' | 'name'> & Partial<Pick<
  GroupSummary,
  | 'systemAccountId'
  | 'systemAccountName'
  | 'ownerSystemAccountId'
  | 'ownerSystemAccountName'
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
>>

export interface AccountGroupOptionSummary extends GroupOptionSummary {
  accountIds: string[]
}
