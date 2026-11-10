import type { AccountGroupBindStatus, AccountStatus, AccountTrafficMigrationSourceStatus, AccountType, AuthorizationStatus, GroupType, ProviderCode, ResourceAccessType } from './base'
import type { RequestQuotaLimits } from './access'
import type { AuthorizationSourceSummary } from './authorizations'
import type { AccountUsageSummary } from './usage-stats'
import type { AccountCapabilityDomain } from '@/views/accounts/accountCapabilityDomains'

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
  | 'audio_speech'
  | 'audio_transcription_json'
  | 'video_create'
  | 'video_get'
  | 'video_content'
  | 'video_cancel'
  | 'audio_job_create'
  | 'audio_job_get'
  | 'audio_job_content'
  | 'audio_job_cancel'
  | 'realtime_session'
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
  | 'audio_speech'
  | 'audio_transcription_json'
  | 'video_create'
  | 'video_get'
  | 'video_content'
  | 'video_cancel'
  | 'audio_job_create'
  | 'audio_job_get'
  | 'audio_job_content'
  | 'audio_job_cancel'
  | 'realtime_session'
>
export type AccountApiKeyRuntimeStatus = 'active' | 'unverified' | 'temporary_unavailable' | 'rate_limited' | 'error' | 'disabled'

export interface AccountCredentials {
  api_key?: string
  api_keys?: string[]
  api_key_strategy?: 'round_robin' | 'weighted_round_robin' | 'failover'
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
  quota_recovery_policy?: Record<string, unknown>
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
  canLock?: boolean
}

export interface AccountOAuthUsageWindow {
  utilization: number
  resetsAt?: string
  remainingSeconds: number
  windowMinutes?: number
}

/** OpenAI Codex OAuth 窗口用量快照（被动响应头采集）。 */
export interface AccountOpenAICodexUsageSnapshot {
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

/** xAI Grok OAuth 订阅周期用量快照（billing/settings 主动查询）。 */
export interface AccountXAIGrokUsageSnapshot {
  kind: 'xai_grok'
  usedPercent?: number
  periodType?: string
  periodStart?: string
  periodEnd?: string
  subscriptionTier?: string
  /** productUsage[] 原样 JSON 字符串，可选展示分产品明细。 */
  productUsage?: string
}

/** Anthropic Claude unified 窗口用量（unified rate limit 响应头被动采集）。窗口形状与 codex 共享投影结构：utilization 为已用百分比（0-100），resetsAt 为 RFC3339。 */
export interface AccountAnthropicUsageWindow {
  utilization?: number
  resetsAt?: string
}

/** Anthropic（Claude OAuth）窗口用量快照（unified rate limit 响应头被动采集）。 */
export interface AccountAnthropicUsageSnapshot {
  kind: 'anthropic_claude'
  fiveHour?: AccountAnthropicUsageWindow
  sevenDay?: AccountAnthropicUsageWindow
}

export type AccountOAuthUsageSnapshot = AccountOpenAICodexUsageSnapshot | AccountXAIGrokUsageSnapshot | AccountAnthropicUsageSnapshot

export type AccountRuntimeAvailabilityStatus = 'normal' | 'degraded' | 'local_suppressed' | 'half_open' | 'precheck_pending' | 'precheck_failed'

export interface AccountRuntimeAvailability {
  status: AccountRuntimeAvailabilityStatus
  reason?: string
  since?: string
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
  | 'source_quality_isolated'
  | 'source_cooldown'
  | 'source_unschedulable'
  | 'instance_expired'
  | 'instance_pending_test'
  | 'instance_disabled'
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
  /** 时间计划闸门（时间计划闸门设计 §5.4）：status='active' 且 gate=0，仅暂停调度，不是停用。 */
  | 'schedule_out_of_window'

export type AccountEffectiveAvailabilityBlockerScope =
  | 'permission'
  | 'authorization'
  | 'source_account'
  | 'account'
  | 'authorized_instance'
  | 'binding'
  | 'api_key_pool'
  | 'runtime'
  /** 时间计划闸门：当前不在允许时段内。 */
  | 'schedule'

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
    kind: 'policy_ttl_expiry' | 'quota_reset' | 'cooldown_expiry' | 'account_expired' | 'authorization_expired' | 'source_expired' | 'schedule_window_open'
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
  concurrencyLimit: number
  todayUsage?: AccountUsageSummary
  usage: AccountUsageSummary
}

export interface AccountModelMapping {
  sourceModel: string
  // M4b 媒体映射族（媒体设计 §9）：video_generation / tts 同族
  // 模型名改写，与后端 write.go accountSourceEndpointFamilies 同枚举。
  sourceEndpointFamily: 'chat_completions' | 'responses' | 'messages' | 'generate_content' | 'stream_generate_content' | 'video_generation' | 'tts'
  upstreamModel: string
  // 上游枚举与后端 accountUpstreamEndpointFamilies 同集（stream_generate_content
  // 仅作 source，媒体族上游面恒为 OpenAI 形态媒体端点）。
  upstreamEndpointFamily: 'chat_completions' | 'responses' | 'messages' | 'generate_content' | 'video_generation' | 'tts'
  enabled: boolean
}

export interface AccountTagSummary {
  id: string
  name: string
  accountCount?: number
  createdAt?: string
  updatedAt?: string
}

export type AccountBalanceBuiltinAdapter = 'sub2api' | 'newapi' | 'openai_billing' | 'litellm' | 'user_balance'
export type AccountBalanceAdapter = 'builtin' | 'custom'
export type AccountBalanceStatus = 'pending' | 'refreshing' | 'fresh' | 'unlimited' | 'unsupported' | 'failed'
export type AccountBalanceScope = 'key' | 'account' | 'unknown'
export type AccountBalanceAggregation = 'sum' | 'shared' | 'unknown'

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
  /** 生成该余额快照的账户配置版本。 */
  configRevision?: number
  remainingUsd?: string
  rawUnit?: 'usd' | 'cny' | 'quota'
  errorMessage?: string
  lastAttemptAt?: string
  lastSuccessAt?: string
  consecutiveTransientFailures?: number
  lastTransientErrorMessage?: string
  lastTransientFailureAt?: string
  /** 多 Key 余额的归属口径和聚合方式；列表接口不包含 keyBalances。 */
  scope?: AccountBalanceScope
  aggregation?: AccountBalanceAggregation
  keyCount?: number
  queriedKeyCount?: number
  keyBalances?: AccountBalanceKeySnapshot[]
}

/** 服务端返回的逐 Key 公开余额摘要，Key 本身必须已经掩码。 */
export interface AccountBalanceKeySnapshot {
  keyFingerprint: string
  maskedKey: string
  status: AccountBalanceStatus
  remainingUsd?: string
  rawUnit?: 'usd' | 'cny' | 'quota'
  scope?: AccountBalanceScope
  basis?: 'api_key_quota' | 'budget' | 'subscription' | 'wallet' | 'custom' | 'unknown'
  errorMessage?: string
  lastAttemptAt?: string
  lastSuccessAt?: string
}

export interface AccountBalanceDetails {
  accountId: string
  configRevision?: number
  keyCount: number
  queriedKeyCount: number
  scope: AccountBalanceScope
  aggregation: AccountBalanceAggregation
  updatedAt?: string
  keyBalances: AccountBalanceKeySnapshot[]
}

export interface AccountTagsUpdateResult {
  id: string
  tags: Array<Pick<AccountTagSummary, 'id' | 'name'>>
}

export interface AccountUsageListSummary {
  requestCount: number
  totalTokens: number
  totalCost: number
  inputTokens?: number
  cacheReadTokens?: number
  /** 后端注入的今日缓存读占比（cache_read ÷ input 分数）；null 表示无有效数据 */
  cacheReadRate?: number | null
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
  status: AccountStatus
  credentials: AccountCredentials
  concurrencyLimit: number
  /** 每分钟请求上限（RPM 设计 §6.1）：0 表示不限制；授权实例继承来源账户值。 */
  rpmLimit: number
  /** 本分钟已派发次数（授权实例按来源账户读）；读失败降级 0。 */
  rpmCurrent?: number
  /** RPM 窗口结束时刻（RFC3339，可空）。 */
  rpmResetAt?: string
  currentConcurrency: number
  runtimeAvailability?: AccountRuntimeAvailability
  circuitSummary?: {
    status: 'normal' | 'verifying' | 'avoided' | 'recovering'
    reason?: 'connect_failed' | 'timeout_before_complete' | 'read_interrupted' | 'incomplete_response' | 'explicit_policy'
    since?: string
    nextCheckAt?: string
  }
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
  proxyProfileId?: string
  proxyProfileName?: string
  proxyProfileType?: 'http' | 'https' | 'socks5' | 'socks5h'
  proxyProfileEnabled?: boolean
  proxyProfileUnavailable?: boolean
  proxyProfileErrorMessage?: string
  // 代理池绑定展示字段（代理池设计 §8.5）：池绑定时 proxyProfile* 为 null，
  // proxyMember* 是当前租约成员；proxyPoolAssignableMemberCount 驱动待分配/待切换/无可用成员三态。
  proxyTargetType?: 'profile' | 'pool'
  proxyPoolId?: string
  proxyPoolName?: string
  proxyPoolEnabled?: boolean
  proxyMemberProfileId?: string
  proxyMemberProfileName?: string
  proxyMemberProfileType?: 'http' | 'https' | 'socks5' | 'socks5h'
  proxyMemberProfileEnabled?: boolean
  proxyPoolAssignableMemberCount?: number
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
  /** 后台自动探测总开关（总开关设计 §2）：授权实例行回显来源账户值，只读；缺省视作开启。 */
  backgroundProbeEnabled?: boolean
  lastHealthCheckAt?: string
  nextHealthCheckAt?: string
  lastHealthSuccessAt?: string
  lastHealthCheckErrorCode?: string
  lastHealthCheckErrorMessage?: string
  lastHealthCheckTraceId?: string
  apiKeyRuntime?: AccountApiKeyRuntimePublicSummary
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
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  authorizationStatus?: AuthorizationStatus
  authorizationExpiresAt?: string
  authorizationLimits?: RequestQuotaLimits
  authorizationQuotaExceeded?: boolean
  authorizationSources?: AuthorizationSourceSummary[]
  /** 运行态锁死状态只读投影（只读展示用，不作为配置提交）。 */
  lockState?: 'UNLOCKED' | 'LOCKED_IDLE' | 'ENGAGED' | 'DEAD_CONFIRMED'
  // List/options endpoints may return only the permissions relevant to that action.
  permissions?: Partial<ResourcePermissions>
  authorizationUsageAvailable?: boolean
  authorizationCount?: number
  authorizationTeamCount?: number
}

export interface AccountListItem {
  id: string
  configRevision?: number
  systemAccountId?: string
  systemAccountName?: string
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  providerCode: ProviderCode
  providerName?: string
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  name: string
  notes?: string
  type: AccountType
  /** 账户能力域（M1 读通道回显）：四域菜单按域请求，响应域与请求一致；缺省视作对话域。 */
  capabilityDomain?: AccountCapabilityDomain
  status: AccountStatus
  apiKeyRuntime?: AccountApiKeyRuntimePublicSummary
  concurrencyLimit: number
  /** 每分钟请求上限（RPM 设计 §6.1）：0 表示不限制；授权实例继承来源账户值。 */
  rpmLimit: number
  /** 本分钟已派发次数（授权实例按来源账户读）；读失败降级 0。 */
  rpmCurrent?: number
  /** RPM 窗口结束时刻（RFC3339，可空）。 */
  rpmResetAt?: string
  currentConcurrency: number
  runtimeAvailability?: AccountRuntimeAvailability
  circuitSummary?: AccountSummary['circuitSummary']
  effectiveAvailability?: AccountEffectiveAvailability
  availabilityPresentation?: AccountAvailabilityPresentation
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  clientCompatibility: AccountClientCompatibility
  tags?: Array<Pick<AccountTagSummary, 'id' | 'name'>>
  healthCheckModel: string
  healthCheckEndpointMode: AccountHealthCheckEndpointMode
  proxyProfileId?: string
  proxyProfileName?: string
  proxyProfileType?: 'http' | 'https' | 'socks5' | 'socks5h'
  proxyProfileEnabled?: boolean
  proxyProfileUnavailable?: boolean
  proxyProfileErrorMessage?: string
  // 代理池绑定展示字段（代理池设计 §8.5）：池绑定时 proxyProfile* 为 null，
  // proxyMember* 是当前租约成员；proxyPoolAssignableMemberCount 驱动待分配/待切换/无可用成员三态。
  proxyTargetType?: 'profile' | 'pool'
  proxyPoolId?: string
  proxyPoolName?: string
  proxyPoolEnabled?: boolean
  proxyMemberProfileId?: string
  proxyMemberProfileName?: string
  proxyMemberProfileType?: 'http' | 'https' | 'socks5' | 'socks5h'
  proxyMemberProfileEnabled?: boolean
  proxyPoolAssignableMemberCount?: number
  schedulable: boolean
  /**
   * 时间计划闸门（时间计划闸门与账户状态解耦设计 §3.1）：null=未配置计划（恒开），
   * 1=当前在允许时段内，0=当前不在允许时段内（仅暂停调度，不改写 status）。
   */
  availabilityScheduleGate?: number | null
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
  lastHealthCheckErrorCode?: string
  lastHealthCheckErrorMessage?: string
  lastHealthCheckTraceId?: string
  streamFailureCount?: number
  streamFailureWindowStartedAt?: string
  balanceQueryEnabled?: boolean
  balanceQueryNextRefreshAt?: string
  balanceSnapshot?: AccountBalanceSnapshot
  lastUsedAt?: string
  todayUsage: AccountUsageListSummary
  /** 网关列表投影按 provider 注入的 OAuth 用量只读快照（gpt→openai_codex，xai→xai_grok，anthropic→anthropic_claude）。 */
  oauthUsage?: AccountOAuthUsageSnapshot
  accessType?: ResourceAccessType
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
  lockState?: 'UNLOCKED' | 'LOCKED_IDLE' | 'ENGAGED' | 'DEAD_CONFIRMED'
  lockDeathTimeoutSeconds?: number
  lockRetryIntervalSeconds?: number
  /** 后台自动探测总开关（总开关设计 §2）：授权实例行回显来源账户值，只读；缺省视作开启。 */
  backgroundProbeEnabled?: boolean
  /** 特供快速恢复（恢复道加速档）：行属性标记；列表投影恒输出（合成行可能缺省）。 */
  expeditedRecoveryEnabled?: boolean
  /** 归属系统账户特供名额超限展示位：归属超限时名下所有行（含未特供行）都为 true。 */
  expeditedOverLimit?: boolean
  permissions?: Partial<Pick<ResourcePermissions,
    | 'canUse'
    | 'canEdit'
    | 'canDelete'
    | 'canReturnAuthorization'
    | 'canAuthorize'
    | 'canViewCredentials'
    | 'canLock'
  >>
}

export interface AccountEditBasicDetail {
  id: string
  configRevision: number
  systemAccountId?: string
  ownerSystemAccountId: string
  providerCode: ProviderCode
  providerProtocolProfileId: string
  /**
   * 健康检查连接 preset（后端读面 currentHealthCheckPreset 投影，omitempty）。
   * T102：详情 providerProtocolProfileId 由探针表反查派生，媒体四档 preset 不在
   * 表内返回空串——前端按本字段经 shared/providerProtocol 反查表补齐供应商身份。
   */
  connectionPresetCode?: string
  protocolCode: string
  protocolVersion: string
  name: string
  notes?: string
  type: AccountType
  credentials: AccountCredentials
  status: AccountStatus
  concurrencyLimit: number
  /** 每分钟请求上限（RPM 设计 §6.3）：0 表示不限制。 */
  rpmLimit: number
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

export interface AccountAdvancedDetail {
  id: string
  configRevision: number
  accessType: 'owner' | 'authorized'
  credentials?: AccountCredentials
  modelMappings: AccountModelMapping[]
  proxyProfileId?: string
  /** 配置的代理池绑定（§8.3）：编辑表单回填配置绑定，proxyPoolName 用于标签注入。 */
  proxyPoolId?: string
  proxyPoolName?: string
  availabilitySchedule?: AccountAvailabilitySchedule
  accountExpiresAt?: string
  temporaryUnavailableContinuousProbeEnabled: boolean
  /** 后台自动探测总开关（总开关设计 §2）：授权实例返回来源账户值，只读。 */
  backgroundProbeEnabled: boolean
  balanceQueryEnabled: boolean
  balanceQueryConfig?: AccountBalanceQueryConfig
  /**
   * Effective rules are display-only metadata.  Account credentials remain the
   * sole source of editable account-local rules.
   */
  effectiveErrorHandlingRules?: AccountEffectiveErrorHandlingRule[]
  /** Effective non-secret quota policy, including inherited source policy for authorized accounts. */
  effectiveQuotaRecoveryPolicy?: Record<string, unknown>
  authorizationInstanceSourceAccountStatus?: AccountStatus
  authorizationInstanceSourceAccountSchedulable?: boolean
  lockState?: 'UNLOCKED' | 'LOCKED_IDLE' | 'ENGAGED' | 'DEAD_CONFIRMED'
  lockDeathTimeoutSeconds?: number
  lockRetryIntervalSeconds?: number
  /** 特供快速恢复：advanced 详情投影，与列表行同名字段恒输出。 */
  expeditedRecoveryEnabled: boolean
  expeditedOverLimit: boolean
}

export interface AccountEffectiveErrorHandlingRule {
  id: string
  source: 'system' | 'account'
  inherited: boolean
  editable: boolean
  enabled: boolean
  name: string
  priority: number
  status_codes?: number[]
  error_codes?: string[]
  error_types?: string[]
  keywords?: string[]
  action: 'retry_next' | 'rate_limited' | 'temp_unschedulable' | 'error_disabled'
  reset_strategy?: 'duration' | 'daily' | 'weekly'
  duration_hours?: number
  daily_reset_hour?: number
  weekly_reset_day?: number
  weekly_reset_hour?: number
  description?: string
}

export interface AccountOAuthReauthorizationContext {
  id: string
  configRevision: number
  oauthType: 'code_assist' | 'google_one' | 'ai_studio'
  clientId?: string
  clientSecret?: string
  quotaProjectId?: string
  projectId?: string
  tierId?: string
  baseUrl?: string
}

export interface AccountCloneContext {
  id: string
  configRevision: number
  providerCode: ProviderCode
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  name: string
  notes?: string
  type: AccountType
  status: AccountStatus
  credentialOptions: AccountCloneCredentialOptions
  concurrencyLimit: number
  /** 每分钟请求上限（RPM 设计 §3 克隆）：克隆是配置复制，不复制运行态计数。 */
  rpmLimit: number
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  clientCompatibility: AccountClientCompatibility
  supportedModels: string[]
  tags: Array<Pick<AccountTagSummary, 'id' | 'name'>>
  healthCheckModel: string
  healthCheckEndpointMode: AccountHealthCheckEndpointMode
  /**
   * 健康检查连接 preset（DR-40）：后端 clone-context 回显，供前端在
   * providerProtocolProfileId 为空（媒体 preset 不在探针表）时反查供应商身份。
   */
  connectionPresetCode?: string
  boundGroupId?: string
  boundGroupName?: string
  modelMappings: AccountModelMapping[]
  proxyProfileId?: string
  /** 来源账户的代理池绑定（§8.4 克隆）：复制配置绑定，不复制租约；proxyPoolName 供前端展示池名。 */
  proxyPoolId?: string
  proxyPoolName?: string
  availabilitySchedule?: AccountAvailabilitySchedule
  accountExpiresAt?: string
  temporaryUnavailableContinuousProbeEnabled: boolean
  /** 后台自动探测总开关（总开关设计 §8 克隆）：继承来源账户值。 */
  backgroundProbeEnabled: boolean
  balanceQueryEnabled: boolean
  balanceQueryConfig?: AccountBalanceQueryConfig
}

export interface AccountCloneCredentialOptions {
  api_key_count?: number
  api_key_strategy?: 'round_robin' | 'weighted_round_robin' | 'failover'
  api_key_weights?: number[]
  base_url?: string
  supported_endpoint_modes?: AccountSupportedEndpointMode[]
  client_id?: string
  quota_project_id?: string
  oauth_type?: 'code_assist' | 'google_one' | 'ai_studio'
  tier_id?: string
  project_id?: string
  service_tier_override?: AccountGptServiceTierOverride
  reasoning_effort_override?: AccountGptReasoningEffortOverride
  error_handling_rules?: unknown[]
  response_inspection_rules?: unknown[]
  quota_recovery_policy?: Record<string, unknown>
}

export interface AccountMutationResult {
  id: string
  configRevision: number
  changedFields: string[]
  authorizationInstancesAffected?: boolean
}

export type OAuthCredentialRotationPayload = Record<string, unknown> & {
  expectedConfigRevision: number
}

export interface OAuthCredentialRotationResult {
  id: string
  configRevision: number
  updatedAt: string
}

export interface AuthorizedAccountDispatchMutationPatch {
  status?: AccountStatus
  schedulable?: boolean
  priority?: number
  superPriorityEnabled?: boolean
  fallbackEnabled?: boolean
  failureStateCleared?: true
}

export interface AuthorizedAccountDispatchMutationResult extends AccountMutationResult {
  patch: AuthorizedAccountDispatchMutationPatch
}

export interface AccountCreateResult {
  id: string
  status: AccountStatus
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

export interface AccountBatchEditField<TValue> {
  enabled: boolean
  value: TValue
}

export interface AccountBatchEditUpdates {
  tags?: AccountBatchEditField<string[]>
  proxyProfileId?: AccountBatchEditField<string | null>
  /** 代理池批量绑定（§8.4）：与 proxyProfileId 互斥；同一勾选状态下两者恰有一个为 null。 */
  proxyPoolId?: AccountBatchEditField<string | null>
  concurrencyLimit?: AccountBatchEditField<number>
  rpmLimit?: AccountBatchEditField<number>
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
  changedFields: string[]
  items: Array<{
    id: string
    configRevision: number
    changedFields: string[]
    /** 逐账户跳过报告（T100）：批量改上游接口能力时多连接/无连接行账户跳过连接行写面（凭据域副本照常写），此处披露原因与动作；其余场景缺省。 */
    notice?: string
  }>
}

/** 列表和状态快照只返回无诊断、无单 Key 信息的公开聚合。 */
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

export interface AccountApiKeyRuntimeResponse {
  accountId: string
  configRevision: number
  items: AccountApiKeyRuntimeDetail[]
}

export interface AccountListResult {
  items: AccountListItem[]
  generatedAt: string
  total: number
  hasMore?: boolean
  page: number
  pageSize: number
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

export type AccountImportSourceMode = 'native' | 'sub2api' | 'newapi' | 'cpa' | 'oneapi'

export interface AccountImportSourceSummary {
  mode: AccountImportSourceMode
  records: number
  accepted: number
  skipped: number
  ignoredFields: number
  messages: string[]
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

/** 导入预览池引用行（§8.4/§9.4）：第一版只读引用不建池；缺失/停用池按条目失败提示。 */
export interface AccountImportPoolItem {
  index: number
  ref?: string
  name?: string
  action: 'create' | 'reuse' | 'skip' | 'failed'
  messages: string[]
  warnings: string[]
  proxyPoolId?: string
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
  /** 每分钟请求上限：仅 >0 时导出（与 concurrencyLimit 同省略规则）；导入缺省落 0。 */
  rpmLimit?: number
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
  /** 池引用预览行（§9.4）：服务端按需返回；缺失时不渲染该表（前端不本地推导）。 */
  pools?: AccountImportPoolItem[]
  messages: string[]
  source: AccountImportSourceSummary
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

// 普通分组可选调度策略（普通分组可选调度策略设计 §2.2）：非空对象必须同时
// 包含两个键；NULL 等价于两项都取默认值。旧 16 键高并发策略已退役。
export interface GroupSchedulingPolicy {
  /** 最大单账户排队阈值：-1 关闭（默认）；1..1000000 生效；0 不是合法值。 */
  defaultSoftConcurrency?: number
  /** 单 IP 并发上限：0 不限制（默认）；1..1000000 生效，超限立即返回 429。 */
  clientIpConcurrencyLimit?: number
}

export type GroupKind = 'standard' | 'super'

export interface GroupSummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  name: string
  // 分组供应商维度已退役：后端列表仍回恒空串 providerCode 作为 Node 契约形状兼容键，前端类型不再声明。
  description?: string
  enabled: boolean
  isDefault: boolean
  groupType: GroupType
  /** 分组形态：普通分组直接拥有账号；超级分组只拥有成员关系（超级分组设计 §3.1）。 */
  groupKind?: GroupKind
  /** 可选调度策略：NULL/缺省 = 两项都取默认值（普通分组可选调度策略设计 §2.2）。 */
  schedulingPolicy?: GroupSchedulingPolicy | null
  accountIds?: string[]
  accountStats: GroupAccountStats
  accessType?: ResourceAccessType
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

export interface GroupListItem extends Omit<
  GroupSummary,
  'accountIds' | 'schedulingPolicy' | 'authorizationLimits' | 'authorizationSources' | 'accountStats' | 'permissions'
> {
  updatedAt: string
  /** 列表项可选调度策略（普通分组可选调度策略设计 §2.3）：供类型列摘要直接使用，不再逐行拉 edit-basic。 */
  schedulingPolicy?: GroupSchedulingPolicy | null
  accountStats: Pick<
    GroupAccountStats,
    'total' | 'available' | 'active' | 'disabled' | 'error' | 'rateLimited' | 'concurrencyLimit'
  > & {
    currentConcurrency?: number
    todayUsage?: AccountUsageSummary
  }
  canEdit: boolean
  canDelete: boolean
  canReturn: boolean
}

// 编辑详情同 GroupSummary：后端仍回恒空串 providerCode 兼容键，前端类型已退役不再声明。
export type GroupEditDetail = Pick<
  GroupSummary,
  'name' | 'description' | 'enabled' | 'groupType' | 'groupKind' | 'schedulingPolicy'
> & { updatedAt: string }

export interface GroupMutationResult {
  id: string
  changedFields: string[]
  updatedAt: string
  /** PATCH changedFields 含 schedulingPolicy 时的最终策略回显（普通分组可选调度策略设计 §2.3）。 */
  schedulingPolicy?: GroupSchedulingPolicy | null
}

export interface SuperGroupMember {
  memberGroupId: string
  memberGroupName: string
  priority: number
  enabled: boolean
  groupEnabled: boolean
}

export interface SuperGroupMembersSummary {
  superGroupId: string
  groupKind: 'super'
  revision: string
  members: SuperGroupMember[]
}

export interface SuperGroupMemberInput {
  memberGroupId: string
  priority: number
  enabled: boolean
}

export interface GroupListResult {
  items: GroupListItem[]
  generatedAt: string
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

// GroupOptionSummary 的字段白名单对应 options 端点（契约矩阵「分组 options」）：
// 大部分键与分组列表共享，`groupAuthorizationId` 仅 options 附带（列表投影
// 不发送），故独立声明而非从 GroupSummary Pick。
export type GroupOptionSummary = Pick<GroupSummary, 'id' | 'name'> & Partial<Pick<
  GroupSummary,
  | 'systemAccountId'
  | 'systemAccountName'
  | 'ownerSystemAccountId'
  | 'ownerSystemAccountName'
  | 'enabled'
  | 'isDefault'
  | 'groupType'
  | 'groupKind'
  | 'schedulingPolicy'
  | 'accessType'
  | 'authorizationExpiresAt'
  | 'authorizationLimits'
  | 'authorizationStatus'
  | 'permissions'
>> & {
  groupAuthorizationId?: string
}

export interface AccountGroupOptionSummary extends GroupOptionSummary {
  accountIds: string[]
}
