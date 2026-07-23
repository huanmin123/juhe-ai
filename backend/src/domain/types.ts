export type ProviderCode = string
export type AccountType = string
export type AccountStatus = 'active' | 'pending_test' | 'disabled' | 'error' | 'rate_limited' | 'temporary_unavailable'
export type AccountApiKeyRuntimeStatus = 'active' | 'temporary_unavailable' | 'rate_limited' | 'error' | 'disabled'
export type AccountTrafficMigrationSourceStatus = 'temporary_unavailable' | 'disabled' | 'unchanged'
export const ACCOUNT_CLIENT_COMPATIBILITIES = ['openai_standard', 'codex_responses'] as const
export type AccountClientCompatibility = typeof ACCOUNT_CLIENT_COMPATIBILITIES[number]
export const CLIENT_COMPATIBILITY_CAPABILITIES = ['openai_standard', 'codex_responses', 'anthropic_native', 'claude_code'] as const
export type ClientCompatibilityCapability = typeof CLIENT_COMPATIBILITY_CAPABILITIES[number]
export type AccountSupportedEndpointMode =
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

export interface PublicAnnouncementListItem {
  id: string
  title: string
  level: AnnouncementLevel
  publishedAt: string
  readAt?: string
}

export interface PublicAnnouncementDetail {
  id: string
  title: string
  content: string
  level: AnnouncementLevel
  publishedAt: string
}

export interface AnnouncementListItem {
  id: string
  title: string
  contentPreview: string
  level: AnnouncementLevel
  status: AnnouncementStatus
  createdBy?: string
  createdByName?: string
  updatedBy?: string
  updatedByName?: string
  publishedAt?: string
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
  items: SystemTeamListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

/** Fields needed by the paged management list only. */
export interface SystemTeamListItem {
  id: string
  name: string
  description?: string
  status: SystemTeamStatus
  memberCount: number
  createdAt: string
}

/** Fields needed by the member drawer only; details are loaded explicitly. */
export interface SystemTeamMemberDetail {
  id: string
  systemAccountId: string
  systemAccountName?: string
  joinedAt: string
}

export interface SystemTeamDetail {
  id: string
  name: string
  description?: string
  status: SystemTeamStatus
  memberCount: number
  members: SystemTeamMemberDetail[]
  createdAt: string
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
  defaultHealthCheckModel: string
  systemDefaultHealthCheckModel?: string
  defaultSupportedModels: string[]
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
  defaultHealthCheckModel: string
  accountTypes: AccountType[]
  capabilities: string[]
  endpointFamilies: ProtocolEndpointFamilyDefinition[]
}

export interface ProviderModelPricing {
  providerCode: ProviderCode
  model: string
  id?: string
  scope?: 'built_in' | 'global' | 'personal'
  status?: 'draft' | 'active' | 'disabled'
  catalogVisible?: boolean
  systemAccountId?: string
  mode?: string
  catalogOrder?: number
  releaseDate?: string
  shutdownDate?: string
  contextWindowTokens?: number
  supportedApiProtocols: Array<'chat_completions' | 'responses' | 'messages' | 'message_token_counting' | 'generate_content' | 'stream_generate_content' | 'count_tokens' | 'embed_content' | 'interactions' | 'completions' | 'images' | 'audio' | 'realtime'>
  inputModalities?: Array<'text' | 'image' | 'audio' | 'video' | 'file'>
  outputModalities?: Array<'text' | 'image' | 'audio' | 'video' | 'file'>
  supportedTools?: string[]
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
  serviceTierPrices?: Record<string, ProviderModelPriceSet>
  imageInputUsdPer1M?: number
  imageOutputUsdPer1M?: number
  audioInputUsdPer1M?: number
  audioOutputUsdPer1M?: number
  outputUsdPerImage?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  maxTokens?: number
  longContextInputTokenThreshold?: number
  longContextInputTokenThresholdInclusive?: boolean
  longContextInputCostMultiplier?: number
  longContextOutputCostMultiplier?: number
  supportsPromptCaching: boolean
  supportsServiceTier: boolean
  supportedServiceTiers?: string[]
  supportedReasoningEfforts?: string[]
  defaultReasoningEffort?: string
  pricingNotes?: string
  capabilityNotes?: string
  notes?: string
  createdAt?: string
  updatedAt?: string
  source: string
}

export interface ProviderModelPriceSet {
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
}

export interface AccountCredentials {
  api_key?: string
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
  service_tier_override?: string
  reasoning_effort_override?: string
  response_inspection_rules?: unknown[]
  [key: string]: unknown
}

export interface AccountUsageSummary {
  requestCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheReadCost: number
  cacheWriteTokens: number
  cacheWrite1hTokens: number
  cacheWriteCost: number
  thinkingTokens: number
  inputImageTokens: number
  outputImageTokens: number
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
  averageFirstTokenMs?: number
  maxFirstTokenMs?: number
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
    averageFirstTokenMs?: number
    maxFirstTokenMs?: number
    averageDurationMs?: number
    maxDurationMs?: number
  }
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

export type AccountRuntimeAvailabilityStatus = 'normal' | 'degraded' | 'local_suppressed' | 'half_open' | 'precheck_pending' | 'precheck_failed'

export interface PublicAccountRuntimeAvailability {
  status: AccountRuntimeAvailabilityStatus
  reason?: string
  since?: string
  probePresentation?: Pick<AccountRuntimeProbePresentation, 'lastObservation' | 'schedule'>
}

export interface AccountRuntimeAvailability extends PublicAccountRuntimeAvailability {
  failureCount?: number
  distinctClientIpCount?: number
  distinctApiKeyCount?: number
  precheckAttemptCount?: number
  localFailureCount?: number
  probePresentation?: AccountRuntimeProbePresentation
}

export type AccountProbeKind =
  | 'health_check'
  | 'activation_check'
  | 'cooldown_retest'
  | 'runtime_probe'
  | 'api_key_retest'
  | 'source_account_probe'

export type AccountProbeResult = 'success' | 'failed'
export type AccountProbeScheduleState = 'scheduled' | 'due_waiting' | 'running' | 'none'

export interface AccountProbeObservation {
  observationId: string
  attemptedAt: string
  result: AccountProbeResult
  httpStatus?: number
  errorCode?: string
  reason?: string
  traceId?: string
}

export interface AccountProbeSchedule {
  state: AccountProbeScheduleState
  nextAttemptAt?: string
}

export interface AccountProbeSummary {
  kind: AccountProbeKind
  lastObservation?: AccountProbeObservation
  schedule: AccountProbeSchedule
}

export type AccountPresentationStatus =
  | 'available' | 'pending_check' | 'check_failed' | 'rate_limited'
  | 'temporarily_unavailable' | 'error' | 'degraded' | 'verifying'
  | 'verification_failed' | 'avoided' | 'key_pool_unavailable' | 'disabled'
  | 'expired' | 'authorization_blocked' | 'binding_missing' | 'permission_denied'
  | 'source_blocked'

export type AccountPresentationAction =
  | 'none' | 'retry_check' | 'restore_account' | 'enable_account' | 'bind_group'
  | 'renew_authorization' | 'contact_authorizer' | 'contact_admin'
  | 'fix_configuration'

export interface AccountAvailabilityPresentation {
  status: AccountPresentationStatus
  label: string
  reason?: string
  action?: AccountPresentationAction
  statusBoundary?: {
    at: string
    kind: 'policy_ttl_expiry' | 'quota_reset' | 'cooldown_expiry' | 'account_expired' | 'authorization_expired' | 'source_expired'
  }
  probe?: AccountProbeSummary
}

export interface AccountLifecyclePresentation {
  accountExpiresAt?: string
  authorizationExpiresAt?: string
  quotaResetAt?: string
}

export interface AccountRuntimeProbePresentation {
  lastObservation?: AccountProbeObservation
  schedule: AccountProbeSchedule
  recoveryAt?: string
  recoveryAtKind?: 'policy_ttl_expiry'
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
  | 'instance_expired'
  | 'instance_disabled'
  | 'instance_pending_test'
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

export type GatewayRequestEndpointFamily =
  | 'chat_completions'
  | 'responses'
  | 'messages'
  | 'generate_content'
  | 'stream_generate_content'
  | 'count_tokens'
  | 'embed_content'
  | 'interactions'
export type AccountModelMappingSourceEndpointFamily = 'chat_completions' | 'responses' | 'messages' | 'generate_content' | 'stream_generate_content'
export type AccountModelMappingUpstreamEndpointFamily = 'chat_completions' | 'responses' | 'messages' | 'generate_content'
export type AccountModelMappingEndpointFamily = AccountModelMappingSourceEndpointFamily | AccountModelMappingUpstreamEndpointFamily

export interface AccountModelMapping {
  sourceModel: string
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily
  upstreamModel: string
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
  enabled: boolean
  runtimeSource?: 'account'
  runtimeRouteRuleId?: string
}

export interface AccountTagSummary {
  id: string
  name: string
  accountCount?: number
  createdAt?: string
  updatedAt?: string
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
  runtimeAvailability?: PublicAccountRuntimeAvailability
  circuitSummary?: PublicAccountCircuitSummary
  effectiveAvailability: AccountEffectiveAvailability
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
  balanceQueryConfig?: import('../modules/accounts/account-balance.types.js').AccountBalanceQueryConfig
  balanceQueryNextRefreshAt?: string
  balanceSnapshot?: import('../modules/accounts/account-balance.types.js').AccountBalanceSnapshot
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
  authorizationInstanceSourceAccountLastErrorTraceId?: string
  authorizationInstanceSourceAccountCooldownRetestLastAt?: string
  authorizationInstanceSourceAccountCooldownRetestLastStatusCode?: number
  authorizationInstanceSourceAccountLastHealthCheckAt?: string
  authorizationInstanceSourceAccountNextHealthCheckAt?: string
  authorizationInstanceSourceAccountLastHealthCheckStatusCode?: number
  authorizationInstanceSourceAccountLastHealthCheckErrorCode?: string
  authorizationInstanceSourceAccountLastHealthCheckErrorMessage?: string
  authorizationInstanceSourceAccountLastHealthCheckTraceId?: string
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

export interface PublicAccountCircuitSummary {
  status: 'normal' | 'verifying' | 'avoided' | 'recovering'
  reason?: 'connect_failed' | 'timeout_before_complete' | 'read_interrupted' | 'incomplete_response' | 'explicit_policy'
  since?: string
  nextCheckAt?: string
}

export type AccountListItem = Omit<AccountSummary,
  | 'credentials'
  | 'supportedModels'
  | 'modelMappings'
  | 'apiKeyRuntimeDetails'
  | 'usage'
  | 'oauthUsage'
  | 'authorizationSources'
  | 'authorizationCount'
  | 'authorizationTeamCount'
  | 'authorizationUsageAvailable'
>

export interface AccountStatusSnapshotItem extends Pick<AccountSummary,
  | 'id'
  | 'status'
  | 'schedulable'
  | 'currentConcurrency'
  | 'cooldownUntil'
  | 'lastErrorCode'
  | 'lastErrorMessage'
  | 'lastErrorTraceId'
  | 'cooldownRetestLastAt'
  | 'cooldownRetestLastStatusCode'
  | 'lastHealthCheckAt'
  | 'nextHealthCheckAt'
  | 'lastHealthCheckStatusCode'
  | 'lastHealthCheckErrorCode'
  | 'lastHealthCheckErrorMessage'
  | 'lastHealthCheckTraceId'
  | 'authorizationStatus'
  | 'authorizationExpiresAt'
  | 'authorizationQuotaExceeded'
  | 'authorizationInstanceSourceAccountStatus'
  | 'authorizationInstanceSourceAccountSchedulable'
  | 'authorizationInstanceSourceAccountExpiresAt'
  | 'authorizationInstanceSourceAccountCooldownUntil'
  | 'authorizationInstanceSourceAccountLastErrorCode'
  | 'authorizationInstanceSourceAccountLastErrorMessage'
  | 'authorizationInstanceSourceAccountLastErrorTraceId'
  | 'authorizationInstanceSourceAccountCooldownRetestLastAt'
  | 'authorizationInstanceSourceAccountCooldownRetestLastStatusCode'
  | 'authorizationInstanceSourceAccountLastHealthCheckAt'
  | 'authorizationInstanceSourceAccountNextHealthCheckAt'
  | 'authorizationInstanceSourceAccountLastHealthCheckStatusCode'
  | 'authorizationInstanceSourceAccountLastHealthCheckErrorCode'
  | 'authorizationInstanceSourceAccountLastHealthCheckErrorMessage'
  | 'authorizationInstanceSourceAccountLastHealthCheckTraceId'
  | 'apiKeyRuntime'
  | 'runtimeAvailability'
  | 'circuitSummary'
  | 'effectiveAvailability'
  | 'availabilityPresentation'
  | 'lastUsedAt'
  | 'todayUsage'
> {}

export interface AccountStatusSnapshotResult {
  generatedAt: string
  runtimeSnapshot: {
    accountConcurrencyAvailable: boolean
    accountRuntimeAvailabilityAvailable: boolean
    accountCircuitSummaryAvailable: boolean
  }
  items: AccountStatusSnapshotItem[]
}

export interface AccountBatchEditTarget {
  accountId: string
  configRevision: number
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
  lastFailureAt?: string
  lastErrorCode?: string
  lastErrorMessage?: string
  lastTraceId?: string
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
  lastTraceId?: string
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

export interface AccountUsageStatsTrendOverview {
  range: AccountUsageStatsRange
  rows: Array<Pick<AccountUsageStatsRow, 'id' | 'name' | 'providerCode' | 'systemAccountId' | 'systemAccountName' | 'ownerSystemAccountId' | 'ownerSystemAccountName' | 'accessType'> & {
    dailyUsage: AccountUsageDailyPoint[]
  }>
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
  upstreamModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
  sourceEndpointFamily?: AccountModelMappingSourceEndpointFamily
  upstreamEndpointFamily?: AccountModelMappingUpstreamEndpointFamily
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
  defaultModel: string
  defaultProfile: ModelCheckProfile
  trustedComparison: ModelCheckTrustedComparisonStatus
}

export interface ModelCheckRunRequest {
  targetType: ModelCheckTargetType
  targetId: string
  model: string
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
  model: string
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
  requestSummary?: Record<string, unknown>
  resultSummary?: Record<string, unknown>
  errorCode?: string
  errorMessage?: string
  createdAt: string
  updatedAt: string
}

export interface ModelCheckRunDetail extends ModelCheckRunSummary {
  requestSummary: Record<string, unknown>
  resultSummary: Record<string, unknown>
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

/** Fields needed by the groups table. Details and edit forms use GroupSummary. */
export interface GroupListItem extends Omit<GroupSummary, 'accountIds' | 'schedulingPolicy' | 'authorizationLimits' | 'authorizationSources' | 'accountStats' | 'permissions'> {
  accountStats: Pick<GroupAccountStats, 'total' | 'available' | 'active' | 'disabled' | 'error' | 'rateLimited' | 'concurrencyLimit'> & {
    currentConcurrency?: number
    currentConcurrencyAvailable?: boolean
    todayUsage?: AccountUsageSummary
  }
  canEdit: boolean
  canDelete: boolean
  canReturn: boolean
  authorizationSourceSummary?: {
    activeSourceCount: number
    hasManual: boolean
    hasTeam: boolean
    teamNames: string[]
  }
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

export interface GroupListPageResult extends Omit<GroupListResult, 'items'> {
  items: GroupListItem[]
}

export interface GroupSelectOption {
  id: string
  name: string
}

export interface GroupAuthorizationOption extends GroupSelectOption {
  canAuthorize: boolean
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

export interface AuthorizationGranteeGroupOptionSummary {
  id: string
  name: string
}

export interface ResourceAuthorizationUsageDetail {
  systemAccountId: string
  systemAccountName?: string
  username?: string
  requestCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheReadCost: number
  cacheWriteTokens: number
  cacheWrite1hTokens: number
  cacheWriteCost: number
  thinkingTokens: number
  inputImageTokens: number
  outputImageTokens: number
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

export interface ResourceAuthorizationListItem {
  id: string
  resourceType: ResourceAuthorizationResourceType
  resourceId: string
  resourceName?: string
  resourceOwnerSystemAccountId: string
  resourceOwnerSystemAccountName?: string
  granteeType: ResourceAuthorizationGranteeType
  granteeSystemAccountId?: string
  granteeSystemAccountName?: string
  granteeUsername?: string
  granteeTeamId?: string
  granteeTeamName?: string
  status: AuthorizationStatus
  remark?: string
  expiresAt?: string
  effectiveSourceType: ResourceAuthorizationSourceType
  effectiveSourceTeamId?: string
  effectiveSourceTeamName?: string
  createdAt: string
  sourceSummary: {
    activeSourceCount: number
    hasManual: boolean
    hasTeam: boolean
    teamSources: Array<{
      sourceTeamId: string
      sourceTeamName?: string
    }>
  }
  permissions: Pick<ResourcePermissions, 'canEdit' | 'canAuthorize'>
}

export interface ResourceAuthorizationListResult {
  items: ResourceAuthorizationListItem[]
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
export type RouteStrategyMode = 'normal' | 'hybrid_smart' | 'weighted' | 'failover' | 'round_robin'
export type ApiKeyClientProfile = 'auto' | 'generic_openai' | 'codex' | 'generic_anthropic' | 'claude_code' | 'generic_gemini' | 'gemini_cli'
export type ApiKeyExplicitHybridRouteAdapterMode = 'direct' | 'bridge'
export type RouteStrategyNormalSchedulingPreference = 'cost_first' | 'speed_first'
export type ApiKeyHybridQualityPreference = 'cost_first' | 'balanced' | 'quality_first'
export type ApiKeyHybridQualityInspectionTriggerMode = 'quality_first_only' | 'risk_based' | 'always_for_hybrid'
export type ApiKeyHybridQualityInspectionFailureAction = 'repair_then_upgrade' | 'upgrade_next_level' | 'retry_same_model' | 'return_error'
export type ApiKeyHybridQualityInspectionUnavailableAction = 'pass_through' | 'return_error'
export type ApiKeyAvailabilityScheduleMode = 'allow_windows'
export type ApiKeyAvailabilityScheduleExceptionAction = 'allow' | 'deny'

export interface ApiKeyHybridLevelRoute {
  minLevel: number
  maxLevel: number
  targetModel: string
  enabled: boolean
}

export interface ApiKeyHybridQualityInspectionConfig {
  enabled: boolean
  scoringGroupId?: string
  scoringModel: string
  triggerMode: ApiKeyHybridQualityInspectionTriggerMode
  maxTriggerLevel: number
  maxRetries: number
  failureAction: ApiKeyHybridQualityInspectionFailureAction
  unavailableAction: ApiKeyHybridQualityInspectionUnavailableAction
}

export interface ApiKeyHybridRoutingConfig {
  scoringGroupId?: string
  scoringModel: string
  scoringContextMode: 'full_request'
  qualityPreference: ApiKeyHybridQualityPreference
  scoringTimeoutMs: number
  scoringFallbackMaxLevel: number
  scoringCacheEnabled: boolean
  scoringCacheTtlSeconds: number
  cacheAffinityEnabled: boolean
  affinityTtlSeconds: number
  switchMinLevelDelta: number
  downgradeConsecutiveLowCount: number
  levelRoutes: ApiKeyHybridLevelRoute[]
  qualityInspection?: ApiKeyHybridQualityInspectionConfig
}

export interface ApiKeyExplicitHybridRouteRule {
  id: string
  enabled: boolean
  priority: number
  sourceClientProfile: ApiKeyClientProfile
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily
  sourceModel?: string
  targetGroupId: string
  targetAccountId?: string
  targetProviderProtocolProfileId?: string
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
  upstreamModel: string
  adapterMode: ApiKeyExplicitHybridRouteAdapterMode
}

export interface RouteStrategyGroupBindingSummary {
  id: string
  groupId: string
  groupName?: string
  providerCode?: ProviderCode
  priority: number
  weight: number
  status: ApiKeyGroupBindingStatus
  groupEnabled: boolean
}

export interface RouteStrategySpeedFirstConfig {
  slowTriggerCount: number
  slowWindowSeconds: number
  recoverySuccessCount: number
  probeIntervalSeconds: number
  degradedTtlSeconds: number
  maxFirstByteRetriesPerRequest: number
}

export interface RouteStrategyNormalRoutingConfig {
  schedulingPreference: RouteStrategyNormalSchedulingPreference
  firstByteDeadlineMs: number
  speedFirstConfig?: RouteStrategySpeedFirstConfig
}

export type RouteStrategyGroupBindingPreview = Pick<RouteStrategyGroupBindingSummary, 'id' | 'groupId' | 'groupName' | 'providerCode' | 'status' | 'groupEnabled'>

export type RouteStrategyStatus = 'active' | 'disabled'

export interface RouteStrategySummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  name: string
  description?: string
  mode: RouteStrategyMode
  status: RouteStrategyStatus
  isDefault: boolean
  normalRoutingConfig?: RouteStrategyNormalRoutingConfig
  hybridRoutingConfig?: ApiKeyHybridRoutingConfig
  groupBindings: RouteStrategyGroupBindingSummary[]
  apiKeyCount?: number
  createdAt: string
  updatedAt: string
}

export interface RouteStrategyListItem {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  name: string
  description?: string
  mode: RouteStrategyMode
  status: RouteStrategyStatus
  isDefault: boolean
  normalRoutingConfig?: RouteStrategyNormalRoutingConfig
  groupBindingPreview: RouteStrategyGroupBindingPreview[]
  bindingCount: number
  apiKeyCount?: number
  createdAt: string
  updatedAt: string
}

export interface RouteStrategyOptionSummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  name: string
  mode: RouteStrategyMode
  status: RouteStrategyStatus
  isDefault: boolean
}

export interface RouteStrategyListItemResult {
  items: RouteStrategyListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface RouteStrategyListResult {
  items: RouteStrategySummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
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
  key?: string
  status: 'active' | 'disabled'
  isDefault?: boolean
  routeStrategyId: string
  routeStrategyName?: string
  routeStrategyMode?: RouteStrategyMode
  routeStrategyStatus?: RouteStrategyStatus
  expiresAt?: string
  quotaLimits: ApiKeyQuotaLimits
  availabilitySchedule?: ApiKeyAvailabilitySchedule
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
