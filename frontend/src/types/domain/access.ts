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

export type RouteStrategyGroupBindingStatus = 'active' | 'disabled'
export type RouteStrategyNormalSchedulingPreference = 'cost_first' | 'speed_first'
export type ApiKeyAvailabilityScheduleMode = 'allow_windows'
export type ApiKeyAvailabilityScheduleExceptionAction = 'allow' | 'deny'

export interface RouteStrategyGroupBindingSummary {
  id: string
  groupId: string
  groupName?: string
  providerCode?: string
  priority: number
  weight: number
  status: RouteStrategyGroupBindingStatus
  groupEnabled: boolean
}

export interface RouteStrategyGroupOption {
  id: string
  name: string
  groupKind: 'standard' | 'super'
  enabled: boolean
}

export interface RouteStrategySpeedFirstConfig {
  slowTriggerCount: number
  slowWindowSeconds: number
  recoverySuccessCount: number
  probeIntervalSeconds: number
  degradedTtlSeconds: number
  maxFirstByteRetriesPerRequest: number
  /** 速度优先可重放文本的总时间软截止（秒）；旧配置无此字段时后端按默认 120 生效，录入范围 60..270。 */
  totalTimeDeadlineSeconds?: number
  /** 压缩与估算输入 ≥10 万 token 大输入请求的总时间软截止（秒）；旧配置无此字段时后端按默认 300 生效，录入范围 300..900。 */
  compactionTotalTimeDeadlineSeconds?: number
}

export interface RouteStrategySpeedFirstLatencyRuntimeSummary {
  runtimeAvailable: boolean
  degradedCount: number
}

export interface RouteStrategySpeedFirstLatencyRuntimeItem {
  accountId: string
  accountName?: string
  scope: {
    groupId: string
  }
  slowCount: number
  slowTriggerCount: number
  slowWindowSeconds: number
  degradedUntil?: string | null
  nextProbeAt?: string | null
  recoverySuccessCount: number
  requiredRecoverySuccessCount: number
  recoveryProbeRoundAttemptCount: number
  recoveryProbeRoundSuccessCount: number
  reason: string
  /** 慢样本维度；旧运行实例可能不返回该字段，缺失时按首字维度语义展示。 */
  dimension?: 'first_byte' | 'total_time'
  /** 运行态命名空间来源；旧运行实例可能不返回该字段，缺失时按策略路由段语义展示。 */
  source?: 'strategy' | 'preference'
  /** 调度偏好 ID；仅 source=preference 的条目携带。 */
  preferenceId?: string
  /** 调度偏好名；仅 source=preference 且服务端解析成功时携带。 */
  preferenceName?: string
}

export interface RouteStrategySpeedFirstLatencyRuntime {
  routeStrategyId: string
  generatedAt: string
  enabled: boolean
  runtimeAvailable: boolean
  degradedCount: number
  items: RouteStrategySpeedFirstLatencyRuntimeItem[]
}

// 调度偏好配置（历史命名 normalRoutingConfig）：自调度偏好通用化起由 normal/weighted/failover/round_robin 四种模式共享。
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

export type RouteStrategyMode = 'normal' | 'round_robin' | 'weighted' | 'failover'
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
  groupBindings: RouteStrategyGroupBindingSummary[]
  updatedAt: string
}

export type RouteStrategyMutableField =
  | 'name'
  | 'description'
  | 'mode'
  | 'status'
  | 'groupBindings'
  | 'normalRoutingConfig'

export interface RouteStrategyMutationRowPatch {
  name?: string
  description?: string | null
  mode?: RouteStrategyMode
  status?: RouteStrategyStatus
  normalRoutingConfig?: RouteStrategyNormalRoutingConfig | null
  bindingCount?: number
  groupBindingPreview?: RouteStrategyGroupBindingPreview[]
  updatedAt?: string
}

export interface RouteStrategyMutationResult {
  id: string
  changedFields: RouteStrategyMutableField[]
  rowPatch: RouteStrategyMutationRowPatch
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
  speedFirstLatencyRuntime?: RouteStrategySpeedFirstLatencyRuntimeSummary
  bindingCount: number
  apiKeyCount: number
  groupBindingPreview: RouteStrategyGroupBindingPreview[]
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

export interface RouteStrategyListResult {
  items: RouteStrategyListItem[]
  generatedAt: string
  total: number
  hasMore?: boolean
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

export type SchedulingPreferenceChatMode = 'cost_first' | 'speed_first'

/** 调度偏好速度优先配置：firstByteDeadlineMs + speedFirstConfig 八字段，写入时按契约九字段全量提交。 */
export interface SchedulingPreferenceSpeedFirstConfig {
  firstByteDeadlineMs: number
  speedFirstConfig: RouteStrategySpeedFirstConfig
}

export interface SchedulingPreferenceSummary {
  id: string
  /** 管理面 owner 摘要；内置行缺席。个人面不返回。 */
  systemAccountId?: string
  systemAccountName?: string
  name: string
  chatMode: SchedulingPreferenceChatMode
  /** 仅 chatMode=speed_first 出现（规范化后对象）。 */
  chatSpeedFirstConfig?: SchedulingPreferenceSpeedFirstConfig
  builtIn: boolean
  /** 未删除且绑定该偏好的 API Key 精确计数（含停用 Key；purpose=chat Key 无绑定）。 */
  boundApiKeyCount: number
  createdAt?: string
  updatedAt: string
}

/** 详情与列表行同型（低增长配置类资源，无独立 detail 投影）。 */
export type SchedulingPreferenceDetail = SchedulingPreferenceSummary

export interface SchedulingPreferenceOptionSummary {
  id: string
  name: string
  chatMode: SchedulingPreferenceChatMode
  builtIn: boolean
  /** 管理面 options 返回 owner；个人面无 owner 字段。 */
  systemAccountId?: string
  systemAccountName?: string
}

export interface SchedulingPreferenceListResult {
  items: SchedulingPreferenceSummary[]
  total: number
  hasMore?: boolean
  page: number
  pageSize: number
}

export interface SchedulingPreferenceRowPatch {
  name?: string
  chatMode?: SchedulingPreferenceChatMode
  chatSpeedFirstConfig?: SchedulingPreferenceSpeedFirstConfig | null
}

export interface SchedulingPreferenceMutationResult {
  id: string
  changedFields: string[]
  rowPatch: SchedulingPreferenceRowPatch
  updatedAt: string
}

export interface ApiKeyUsageListSummary {
  requestCount: number
  totalTokens: number
  totalCost: number
}

export interface ApiKeySummary {
  id: string
  revision: string
  systemAccountId?: string
  systemAccountName?: string
  name: string
  description?: string
  keyPrefix: string
  keySuffix: string
  status: 'active' | 'disabled'
  isDefault?: boolean
  purpose: 'general' | 'chat'
  routeStrategyId: string
  routeStrategyName?: string
  routeStrategyMode?: RouteStrategyMode
  routeStrategyStatus?: RouteStrategyStatus
  /** 绑定调度偏好 ID；null/缺失表示历史「沿用策略路由（迁移期）」，不得回显为成本优先。 */
  schedulingPreferenceId?: string | null
  schedulingPreferenceName?: string
  schedulingPreferenceBuiltIn?: boolean
  expiresAt?: string
  quotaLimits: ApiKeyQuotaLimits
  availabilitySchedule?: ApiKeyAvailabilitySchedule
  usage: ApiKeyUsageListSummary
}

export interface ApiKeyListResult {
  items: ApiKeySummary[]
  total: number
  hasMore?: boolean
  page: number
  pageSize: number
}

export interface CreatedApiKey {
  id: string
  key: string
  keyPrefix: string
  keySuffix: string
  revision: string
}

export interface ApiKeyMutationRowPatch {
  revision: string
  name?: string
  description?: string | null
  keyPrefix?: string
  keySuffix?: string
  status?: 'active' | 'disabled'
  routeStrategyId?: string
  routeStrategyName?: string
  routeStrategyMode?: RouteStrategyMode
  routeStrategyStatus?: RouteStrategyStatus
  schedulingPreferenceId?: string | null
  schedulingPreferenceName?: string
  schedulingPreferenceBuiltIn?: boolean
  expiresAt?: string | null
  quotaLimits?: ApiKeyQuotaLimits
  availabilitySchedule?: ApiKeyAvailabilitySchedule | null
}

export interface ApiKeyMutationResult {
  id: string
  revision: string
  changedFields: string[]
  rowPatch: ApiKeyMutationRowPatch
}

export interface ApiKeySecretResult {
  key: string
}

export type ApiKeyQuotaLimit = RequestQuotaLimit
export type ApiKeyHourlyQuotaLimit = RequestHourlyQuotaLimit
export type ApiKeyQuotaLimits = RequestQuotaLimits

export interface OpenAIAuthURLResult {
  authUrl: string
  sessionId: string
}

export interface OAuthAuthURLResult {
  authUrl: string
  sessionId: string
}

export interface ProxyProfileSummary {
  id: string
  name: string
  description?: string
  type: 'http' | 'https' | 'socks5' | 'socks5h' | string
  host: string
  port: number
  username?: string
  enabled: boolean
  testStatus: string
  latencyMs?: number
  outboundIp?: string
  outboundRegion?: string
  lastTestMessage?: string
  lastTestedAt?: string
  updatedAt: string
}

export interface ProxyProfileMutationValues {
  name?: string
  description?: string | null
  type?: string
  host?: string
  port?: number
  username?: string | null
  enabled?: boolean
  testStatus?: string
  latencyMs?: number | null
  outboundIp?: string | null
  outboundRegion?: string | null
  lastTestMessage?: string | null
  lastTestedAt?: string | null
}

export interface ProxyProfileMutationResult {
  id: string
  updatedAt: string
  changed: boolean
  values: ProxyProfileMutationValues
}

export type ProxyProfileOptionSummary = Pick<ProxyProfileSummary, 'id' | 'name' | 'type' | 'enabled'>

export interface ProxyProfileListResult {
  items: ProxyProfileSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export type ProxyTestItemStatus = 'passed' | 'warning' | 'failed' | 'unknown'
export type ProxyTestOverallStatus = 'passed' | 'warning' | 'failed' | 'unknown'

export interface ProxyTestItem {
  name: string
  status: ProxyTestItemStatus
  httpStatus?: number
  latencyMs?: number
  message: string
  targetUrl?: string
}

export interface ProxyTestReport {
  proxyId: string
  proxyName: string
  score: number
  grade: string
  status: ProxyTestOverallStatus
  passedCount: number
  warningCount: number
  failedCount: number
  outboundIp?: string
  outboundRegion?: string
  baseLatencyMs?: number
  testedAt: string
  items: ProxyTestItem[]
  message: string
}
