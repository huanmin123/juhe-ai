export type ProviderCode = string
export type AccountType = string
export type AccountStatus = 'active' | 'pending_test' | 'disabled' | 'error' | 'rate_limited' | 'temporary_unavailable' | 'quality_isolated'
export type AccountApiKeyRuntimeStatus = 'active' | 'unverified' | 'temporary_unavailable' | 'rate_limited' | 'error' | 'disabled'
export type AccountTrafficMigrationSourceStatus = 'temporary_unavailable' | 'disabled' | 'unchanged'
export const ACCOUNT_CLIENT_COMPATIBILITIES = ['openai_standard', 'codex_responses'] as const
export type AccountClientCompatibility = typeof ACCOUNT_CLIENT_COMPATIBILITIES[number]
export const CLIENT_COMPATIBILITY_CAPABILITIES = ['openai_standard', 'codex_responses', 'anthropic_native', 'claude_code'] as const
export type ClientCompatibilityCapability = typeof CLIENT_COMPATIBILITY_CAPABILITIES[number]
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
  | 'images_json'
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
export type UserRequestLimitWindow = 'perMinute' | 'perDay' | 'perWeek' | 'perMonth'
export type UserRequestLimitSource = 'global' | 'user'

export interface UserRequestLimits {
  perMinute?: number
  perDay?: number
  perWeek?: number
  perMonth?: number
  expiresOn?: string
}

export interface EffectiveUserRequestLimitValue {
  limit: number
  source: UserRequestLimitSource
}

export interface EffectiveUserRequestLimits {
  perMinute: EffectiveUserRequestLimitValue
  perDay: EffectiveUserRequestLimitValue
  perWeek: EffectiveUserRequestLimitValue
  perMonth: EffectiveUserRequestLimitValue
  timezone: string
  overrideExpiresOn?: string
  overrideActive: boolean
}
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
  requestLimits?: UserRequestLimits
  lastLoginAt?: string
  createdAt: string
  updatedAt: string
}

export type SystemAccountPrincipalSummary = Pick<SystemAccountSummary, 'id' | 'username' | 'displayName' | 'status'>
export type SystemAccountListItem = Omit<SystemAccountSummary, 'createdAt' | 'updatedAt'> & {
  editVersion: string
}

export type SystemAccountMutationResult = {
  id: string
  updatedAt: string
  apiKeyValidationCacheInvalidationFailed?: boolean
} & Partial<Pick<SystemAccountSummary,
  'displayName'
  | 'role'
  | 'status'
  | 'mustChangePassword'
  | 'imageGenerationEnabled'
>> & {
  description?: string | null
  requestLimits?: UserRequestLimits | null
}

export interface SystemAccountOptionSummary {
  id: string
  name: string
  disabledReason?: 'account_disabled'
}

export interface CurrentUserSummary {
  id: string
  username: string
  displayName: string
  role: SystemAccountRole
  mustChangePassword: boolean
}

export interface UserDefaultGroupReference {
  id: string
  name: string
}

export interface UserDefaultRouteStrategyReference {
  id: string
  name: string
  mode: RouteStrategyMode
  status: RouteStrategyStatus
}

export interface UserProviderDefaultReference {
  providerCode: ProviderCode
  defaultGroup: UserDefaultGroupReference
  defaultRouteStrategy?: UserDefaultRouteStrategyReference
}

export interface UserReferenceData {
  systemAccountId: string
  providerDefaults: UserProviderDefaultReference[]
  preferredDefaultRouteStrategy?: UserDefaultRouteStrategyReference
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

export interface AnnouncementEditDetail {
  id: string
  title: string
  content: string
  level: AnnouncementLevel
  status: AnnouncementStatus
  revision: string
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
  contentTruncated: boolean
  level: AnnouncementLevel
  status: AnnouncementStatus
  updatedByName?: string
  publishedAt?: string
  revision: string
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
  updatedAt: string
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
  createdAt: string
  updatedAt: string
}

export interface SystemTeamMembersResult {
  id: string
  memberCount: number
  updatedAt: string
  items: SystemTeamMemberDetail[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface SystemTeamMemberHistoryItem extends SystemTeamMemberDetail {
  status: 'removed'
  removedAt?: string
}

export interface SystemTeamMemberHistoryResult {
  id: string
  items: SystemTeamMemberHistoryItem[]
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
  defaultHealthCheckModel: string
  systemDefaultHealthCheckModel?: string
  defaultSupportedModels: string[]
  accountTypes: AccountType[]
  capabilities: string[]
  protocolProfiles: ProviderProtocolProfileDefinition[]
}

/** Fields rendered by the provider catalogue list; protocol profiles are loaded on demand. */
export interface ProviderListItem {
  id: string
  code: ProviderCode
  name: string
  parentCode?: ProviderCode
  description?: string
  enabled: boolean
  protocolCode: string
  baseUrl: string
  defaultHealthCheckModel: string
  defaultSupportedModels: string[]
  accountTypes: AccountType[]
  capabilities: string[]
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
  cacheStorageUsdPer1MPerHour?: number
  serviceTierPrices?: Record<string, ProviderModelPriceSet>
  imageInputUsdPer1M?: number
  cachedImageInputUsdPer1M?: number
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
  sourcePricingCurrency?: string
  sourceExchangeRateToUsd?: number
  sourceExchangeRateDate?: string
  sourcePricingNote?: string
  catalogDisplay?: ProviderModelCatalogDisplaySection[]
  createdAt?: string
  updatedAt?: string
  source: string
}

export interface ProviderModelCatalogDisplaySection {
  key: string
  label: string
  items: ProviderModelCatalogDisplayItem[]
}

export interface ProviderModelCatalogDisplayItem {
  key: string
  label: string
  format: 'usd_per_1m_tokens' | 'usd_per_image' | 'usd_per_1m_token_hour' | 'tokens' | 'multiplier' | 'text'
  value: number | string
}

export interface ProviderModelPriceSet {
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
  cacheStorageUsdPer1MPerHour?: number
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
  providerCode: ProviderCode
  systemAccountName?: string
  ownerSystemAccountName?: string
  accessType?: ResourceAccessType
}

export interface AiPerformanceAccountOption {
  id: string
  name: string
  providerCode: ProviderCode
  systemAccountName?: string
  ownerSystemAccountName?: string
  accessType?: ResourceAccessType
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
  providerCode: string
  points: AiPerformancePoint[]
}

export interface AiPerformanceSummary {
  requestCount: number
  averageFirstTokenMs?: number
  maxFirstTokenMs?: number
  averageDurationMs?: number
  maxDurationMs?: number
}

export interface AiPerformanceBase {
  range: AccountUsageStatsRange
  summary: AiPerformanceSummary
  accounts: AiPerformanceAccount[]
  hourlySeries: AiPerformanceAccountSeries[]
}

export interface AiPerformanceSeries {
  range: AccountUsageStatsRange
  accounts: AiPerformanceAccount[]
  hourlySeries: AiPerformanceAccountSeries[]
}

export interface AiPerformanceOverview {
  range: AccountUsageStatsRange
  defaultAccounts: AiPerformanceAccount[]
  selectedAccounts: AiPerformanceAccount[]
  accounts: AiPerformanceAccount[]
  hourlySeries: AiPerformanceAccountSeries[]
  summary: AiPerformanceSummary
}

export type AiHealthHourStatus = 'success' | 'failure' | 'unknown'

export interface AiHealthHourPoint {
  statHour: string
  status: AiHealthHourStatus
}

export interface AiHealthHourDetail extends AiHealthHourPoint {
  lastObservedAt?: string
  statusCode?: number
  errorCode?: string
  errorMessage?: string
}

export interface AiHealthAccountRow {
  id: string
  name: string
  providerCode: ProviderCode
  status: AccountStatus
  systemAccountName?: string
  lastHealthCheckAt?: string
  lastHealthSuccessAt?: string
  nextHealthCheckAt?: string
  latestStatus: AiHealthHourStatus
  successHours: number
  failureHours: number
  unknownHours: number
  healthRate?: number
  hours: AiHealthHourPoint[]
}

export interface AiHealthListResult {
  items: AiHealthAccountRow[]
  hasMore: boolean
  page: number
  pageSize: number
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
  probePresentation?: Pick<AccountRuntimeProbePresentation, 'lastObservation' | 'schedule' | 'recoveryAt' | 'recoveryAtKind'>
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
  | 'source_quality_isolated'
  | 'source_cooldown'
  | 'source_unschedulable'
  | 'instance_expired'
  | 'instance_disabled'
  | 'instance_pending_test'
  | 'instance_error'
  | 'instance_rate_limited'
  | 'instance_temporary_unavailable'
  | 'instance_quality_isolated'
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
  /** Internal J1/Gateway stale-work fence; never a user-editable setting. */
  dispatchRevision?: number
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
  cooldownRetestGeneration?: string
  cooldownRetestDispatchRevision?: number
  cooldownRetestSourceConfigRevision?: number
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
  /** Physical credential-source revision for authorized-account J1 fences. */
  sourceConfigRevision?: number
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

/** Fields required to initialize the owner account basic edit form. */
export interface AccountEditBasicDetail {
  id: string
  configRevision: number
  systemAccountId?: string
  ownerSystemAccountId: string
  providerCode: ProviderCode
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  name: string
  notes?: string
  type: AccountType
  credentials: AccountCredentials
  status: AccountStatus
  concurrencyLimit: number
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  clientCompatibility: AccountClientCompatibility
  supportedModels: string[]
  tags: Array<Pick<AccountTagSummary, 'id' | 'name'>>
  healthCheckModel: string
  healthCheckEndpointMode: AccountHealthCheckEndpointMode
  boundGroupId?: string
  boundGroupName?: string
}

export interface PublicAccountCircuitSummary {
  status: 'normal' | 'verifying' | 'avoided' | 'recovering'
  reason?: 'connect_failed' | 'timeout_before_complete' | 'read_interrupted' | 'incomplete_response' | 'explicit_policy'
  since?: string
  nextCheckAt?: string
}

export interface AccountListUsageSummary {
  requestCount: number
  totalTokens: number
  totalCost: number
}

export interface AccountListPermissions {
  canUse: boolean
  canEdit: boolean
  canDelete: boolean
  canReturnAuthorization: boolean
  canAuthorize: boolean
  canViewCredentials: boolean
}

/** Exact management-list response. Edit, credentials, model catalog and runtime detail fields belong to dedicated endpoints. */
export interface AccountListItem {
  id: string
  configRevision: number
  systemAccountId?: string
  systemAccountName?: string
  ownerSystemAccountId: string
  ownerSystemAccountName?: string
  providerCode: ProviderCode
  providerName: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  name: string
  notes?: string
  type: AccountType
  status: AccountStatus
  /** 公共 Key 池聚合运行态；诊断和 Key 明细只允许专用 owner 端点返回。 */
  apiKeyRuntime?: AccountApiKeyRuntimePublicSummary
  concurrencyLimit: number
  currentConcurrency: number
  runtimeAvailability?: PublicAccountRuntimeAvailability
  circuitSummary?: PublicAccountCircuitSummary
  effectiveAvailability: AccountEffectiveAvailability
  availabilityPresentation?: AccountAvailabilityPresentation
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  clientCompatibility: AccountClientCompatibility
  tags: Array<Pick<AccountTagSummary, 'id' | 'name'>>
  healthCheckModel: string
  healthCheckEndpointMode: AccountHealthCheckEndpointMode
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
  lastHealthCheckAt?: string
  nextHealthCheckAt?: string
  lastHealthSuccessAt?: string
  healthCheckFailureCount?: number
  healthCheckFailureStartedAt?: string
  lastHealthCheckStatusCode?: number
  lastHealthCheckErrorCode?: string
  lastHealthCheckErrorMessage?: string
  lastHealthCheckTraceId?: string
  streamFailureCount?: number
  streamFailureWindowStartedAt?: string
  balanceQueryEnabled?: boolean
  balanceQueryNextRefreshAt?: string
  balanceSnapshot?: import('../modules/accounts/account-balance.types.js').AccountBalanceSnapshot
  lastUsedAt?: string
  todayUsage: AccountListUsageSummary
  accessType: ResourceAccessType
  accountAuthorizationId?: string
  authorizationInstanceSourceAccountId?: string
  authorizationInstanceSourceAccountStatus?: AccountStatus
  authorizationInstanceSourceAccountSchedulable?: boolean
  authorizationInstanceSourceAccountExpiresAt?: string
  authorizationInstanceSourceAccountCooldownUntil?: string
  authorizationInstanceSourceAccountLastErrorCode?: string
  authorizationInstanceSourceAccountLastErrorMessage?: string
  boundGroupId?: string
  boundGroupName?: string
  groupBindStatus?: AccountGroupBindStatus
  bindingSystemAccountId?: string
  authorizationStatus?: AuthorizationStatus
  authorizationExpiresAt?: string
  authorizationLimits?: RequestQuotaLimits
  authorizationQuotaExceeded?: boolean
  permissions: AccountListPermissions
}

export interface AccountStatusSnapshotItem extends Pick<AccountListItem,
  | 'id'
  | 'status'
  | 'schedulable'
  | 'currentConcurrency'
  | 'cooldownUntil'
  | 'lastErrorCode'
  | 'lastErrorMessage'
  | 'lastErrorTraceId'
  | 'cooldownRetestFailureCount'
  | 'cooldownRetestObservationStartedAt'
  | 'cooldownRetestLastAt'
  | 'cooldownRetestLastStatusCode'
  | 'lastHealthCheckAt'
  | 'nextHealthCheckAt'
  | 'lastHealthSuccessAt'
  | 'healthCheckFailureCount'
  | 'healthCheckFailureStartedAt'
  | 'lastHealthCheckStatusCode'
  | 'lastHealthCheckErrorCode'
  | 'lastHealthCheckErrorMessage'
  | 'lastHealthCheckTraceId'
  | 'streamFailureCount'
  | 'streamFailureWindowStartedAt'
  | 'authorizationStatus'
  | 'authorizationExpiresAt'
  | 'authorizationLimits'
  | 'authorizationQuotaExceeded'
  | 'authorizationInstanceSourceAccountStatus'
  | 'authorizationInstanceSourceAccountSchedulable'
  | 'authorizationInstanceSourceAccountExpiresAt'
  | 'authorizationInstanceSourceAccountCooldownUntil'
  | 'authorizationInstanceSourceAccountLastErrorCode'
  | 'authorizationInstanceSourceAccountLastErrorMessage'
  | 'balanceQueryEnabled'
  | 'balanceQueryNextRefreshAt'
  | 'balanceSnapshot'
  | 'runtimeAvailability'
  | 'circuitSummary'
  | 'apiKeyRuntime'
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

export type AccountBatchEditContextField =
  | 'supportedModels'
  | 'modelMappings'
  | 'supportedEndpointModes'

export interface AccountBatchEditContextItem {
  id: string
  configRevision: number
  providerCode: ProviderCode
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  type: AccountType
  supportedModels?: string[]
  modelMappings?: AccountModelMapping[]
  supportedEndpointModes?: AccountSupportedEndpointMode[]
}

export interface AccountBatchEditResult {
  batchId: string
  changedFields: string[]
  items: Array<{
    id: string
    configRevision: number
    changedFields: string[]
  }>
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

/** 列表和状态快照可返回的 Key 池聚合运行态，不含失败诊断或单 Key 信息。 */
export interface AccountApiKeyRuntimePublicSummary {
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

export type AccountUsageStatsOption = Pick<AccountUsageStatsRow,
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
> & { providerName: string }

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

export type AccountUsageStatsListResult = Omit<AccountUsageStatsOverview, 'summary'>

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
  queuedDeadlineAt?: string
  startedAt?: string
  finishedAt?: string
  updatedAt: string
}

export type ModelCheckTargetType = 'account'
export type ModelCheckProfile = 'quick' | 'full'
export type ModelCheckTriggerKind = 'manual' | 'scheduled' | 'quality_recovery'
export type ModelQualityPenaltyAction = 'disable' | 'fallback' | 'quality_isolate'
export type ModelQualityEnforcementResult = 'not_triggered' | 'applied' | 'already_effective' | 'skipped' | 'stale' | 'pending_retry' | 'failed'
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

export type ModelCheckAccountOption = Pick<AccountOptionSummary, 'id' | 'name' | 'providerCode' | 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion'> & {
  modelCheckModels?: string[]
}

export interface ModelCheckRunRequest {
  targetType: ModelCheckTargetType
  targetId: string
  model: string
  profile?: ModelCheckProfile
  trustedComparison?: boolean
  trustedComparisonAccountId?: string
}

export interface ModelQualityPolicy {
  systemAccountId: string
  revision: number
  profile: ModelCheckProfile
  manualEnforcementEnabled: boolean
  penaltyThreshold: number
  penaltyAction: ModelQualityPenaltyAction
  recoveryIntervalMinutes: number
  createdAt?: string
  updatedAt?: string
}

export interface ModelQualityPolicyUpdateInput {
  expectedRevision: number
  profile?: ModelCheckProfile
  manualEnforcementEnabled?: boolean
  penaltyThreshold?: number
  penaltyAction?: ModelQualityPenaltyAction
  recoveryIntervalMinutes?: number
}

export interface ModelQualitySchedule {
  id: string
  systemAccountId: string
  accountId: string
  accountName?: string
  providerCode?: string
  model: string
  intervalMinutes: number
  profile: ModelCheckProfile
  penaltyThreshold: number
  penaltyAction: ModelQualityPenaltyAction
  recoveryIntervalMinutes: number
  enabled: boolean
  revision: number
  nextRunAt: string
  lastRunId?: string
  lastRunAt?: string
  lastRunStatus?: Exclude<ModelCheckRunStatus, 'running'>
  currentEnforcementAction?: ModelQualityPenaltyAction
  currentEnforcementRecoveryDueAt?: string
  createdAt: string
  updatedAt: string
}

export interface ModelQualityScheduleListResult {
  items: ModelQualitySchedule[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface ModelQualityPolicySnapshot {
  policyRevision: number
  configSource?: 'manual' | 'schedule'
  profile: ModelCheckProfile
  manualEnforcementEnabled: boolean
  threshold: number
  action: ModelQualityPenaltyAction
  recoveryIntervalMinutes: number
  scheduleId?: string
  accountConfigRevision: number
}

export interface ModelQualityDecision {
  triggerKind: ModelCheckTriggerKind
  triggered: boolean
  hardFailure: boolean
  threshold: number
  score: number
  configuredAction: ModelQualityPenaltyAction
  result: ModelQualityEnforcementResult
  reasonCodes: string[]
  beforeStatus?: AccountStatus
  afterStatus?: AccountStatus
  recoveryDueAt?: string
  enforcementId?: string
  generation?: number
  healthSyncResult?: 'applied' | 'pending_retry' | 'failed'
  healthStatHour?: string
  message: string
  decidedAt: string
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
  triggerKind: ModelCheckTriggerKind
  scheduleId?: string
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
  policySnapshot?: ModelQualityPolicySnapshot
  qualityDecision?: ModelQualityDecision
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

export type ModelCheckRunListItem = Pick<ModelCheckRunSummary,
  | 'id'
  | 'systemAccountId'
  | 'providerCode'
  | 'targetType'
  | 'targetId'
  | 'targetName'
  | 'model'
  | 'profile'
  | 'triggerKind'
  | 'trustedComparison'
  | 'level'
  | 'score'
  | 'maxScore'
  | 'status'
  | 'message'
  | 'durationMs'
  | 'errorMessage'
  | 'createdAt'
>

export interface ModelCheckRunListResult {
  items: ModelCheckRunListItem[]
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
  updatedAt: string
  accountStats: Pick<GroupAccountStats, 'total' | 'available' | 'active' | 'disabled' | 'error' | 'rateLimited' | 'concurrencyLimit'> & {
    currentConcurrency?: number
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

/** Exact projection consumed by the group edit form. */
export interface GroupEditDetail {
  name: string
  providerCode: ProviderCode
  description?: string
  enabled: boolean
  groupType: GroupType
  schedulingPolicy?: GroupSchedulingPolicy
  updatedAt: string
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
  generatedAt?: string
}

export interface GroupSelectOption {
  id: string
  name: string
}

export interface RouteStrategyGroupOption extends GroupSelectOption {
  providerCode: string
  enabled: boolean
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
  limits?: RequestQuotaLimits
  resourceAccountExpiresAt?: string
  effectiveSourceType: ResourceAuthorizationSourceType
  effectiveSourceTeamId?: string
  effectiveSourceTeamName?: string
  createdAt: string
  updatedAt: string
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

export interface ResourceAuthorizationMutationResult {
  id: string
  status: AuthorizationStatus
  expiresAt: string | null
  limits: RequestQuotaLimits | null
  updatedAt: string
}

export interface ResourceAuthorizationTerminalMutationResult {
  id: string
  status: Extract<AuthorizationStatus, 'revoked' | 'returned'>
  updatedAt: string
}

export interface ResourceAuthorizationCreateMutationResult {
  item: ResourceAuthorizationListItem
  created: boolean
  previousStatus?: AuthorizationStatus
}

export interface ResourceAuthorizationListResult {
  items: ResourceAuthorizationListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export type ResourceAuthorizationUsageSummary = ResourceAuthorizationSummary

export interface AuthorizationUsageRowSummary {
  requestCount: number
  totalTokens: number
  totalCost: number
}

export interface AuthorizationUsageAggregateSummary extends AuthorizationUsageRowSummary {
  inputTokens: number
  cacheWriteTokens: number
  lastUsedAt?: string
}

export interface AuthorizationTeamUsageRow {
  id: string
  teamId: string
  teamName: string
  resourceType?: ResourceAuthorizationResourceType
  resourceId?: string
  resourceName?: string
  accountOwnerSystemAccountId?: string
  accountOwnerSystemAccountName?: string
  usage: AuthorizationUsageRowSummary
  lastUsedAt?: string
}

export interface AuthorizationTeamUsageRowsResult {
  range: AccountUsageStatsRange
  rows: AuthorizationTeamUsageRow[]
  total: number
  page: number
  pageSize: number
  hasMore: boolean
}

export interface AuthorizationTeamUsageSummary {
  range: AccountUsageStatsRange
  summary: AuthorizationUsageAggregateSummary
}

export interface AuthorizationUserUsageRow {
  id: string
  userName: string
  username?: string
  teamNames?: string[]
  resourceType?: ResourceAuthorizationResourceType
  resourceName?: string
  accountOwnerSystemAccountName?: string
  usage: AuthorizationUsageRowSummary
  lastUsedAt?: string
}

export interface AuthorizationUserUsageRowsResult {
  range: AccountUsageStatsRange
  rows: AuthorizationUserUsageRow[]
  total: number
  page: number
  pageSize: number
  hasMore: boolean
}

export interface AuthorizationUserUsageSummary {
  range: AccountUsageStatsRange
  summary: AuthorizationUsageAggregateSummary
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

export type RouteStrategyNormalRoutingConfig =
  | {
      schedulingPreference: 'cost_first'
      firstByteDeadlineMs?: never
      speedFirstConfig?: never
    }
  | {
      schedulingPreference: 'speed_first'
      firstByteDeadlineMs: number
      speedFirstConfig: RouteStrategySpeedFirstConfig
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

export interface RouteStrategyEditBasicDetail {
  id: string
  systemAccountId?: string
  name: string
  description?: string
  mode: RouteStrategyMode
  status: RouteStrategyStatus
  isDefault: boolean
  normalRoutingConfig?: RouteStrategyNormalRoutingConfig
  hybridRoutingConfig?: ApiKeyHybridRoutingConfig
  groupBindings: RouteStrategyGroupBindingSummary[]
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
  bindingCount?: number
  apiKeyCount?: number
  groupBindingPreview?: RouteStrategyGroupBindingPreview[]
  createdAt: string
  updatedAt: string
}

export interface RouteStrategyListSnapshotItem {
  id: string
  bindingCount: number
  apiKeyCount: number
  groupBindingPreview: RouteStrategyGroupBindingPreview[]
}

export interface RouteStrategyListSnapshotResult {
  generatedAt: string
  items: RouteStrategyListSnapshotItem[]
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

export type CompleteRouteStrategyListItem = Omit<RouteStrategyListItem, 'bindingCount' | 'apiKeyCount' | 'groupBindingPreview'> & {
  bindingCount: number
  apiKeyCount: number
  groupBindingPreview: RouteStrategyGroupBindingPreview[]
}

export interface CompleteRouteStrategyListItemResult extends Omit<RouteStrategyListItemResult, 'items'> {
  items: CompleteRouteStrategyListItem[]
  generatedAt: string
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
  purpose: 'general' | 'chat'
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
