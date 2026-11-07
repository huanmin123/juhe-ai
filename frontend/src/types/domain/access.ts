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

// ---- 安全策略（API Key 安全策略与出站内容脱敏设计 §3/§4）----

export type SecurityPolicyRuleType =
  | 'phone'
  | 'id_card'
  | 'bank_card'
  | 'email'
  | 'ipv4'
  | 'secret'
  | 'custom_regex'
  | 'custom_keywords'

export type SecurityPolicyRuleAction = 'mask' | 'placeholder'

/** 单条规则（rules_json.rules[]）。内置类型 pattern 由服务端代码提供，请求不携带、不可改写。 */
export interface SecurityPolicyRule {
  id: string
  /** placeholder 标签；缺省按 type 默认词表（PHONE / ID_CARD / BANK_CARD / EMAIL / IPV4 / SECRET / CUSTOM）。 */
  label?: string
  type: SecurityPolicyRuleType
  action: SecurityPolicyRuleAction
  /** custom_regex 专属：RE2 语法，≤256 字节。 */
  pattern?: string
  /** custom_keywords 专属：字面关键词，≤100 个，单个 ≤64 字节，大小写敏感。 */
  keywords?: string[]
  /** mask 动作可选保留位数（0..8）；缺省按 type 默认，自定义规则默认整体 ****。 */
  maskKeep?: number
}

export interface SecurityPolicyListItem {
  id: string
  /** 管理面 owner 摘要；内置行缺席。个人面不返回。 */
  systemAccountId?: string
  systemAccountName?: string
  name: string
  description?: string
  rules: SecurityPolicyRule[]
  builtIn: boolean
  /** 未删除且绑定该策略的 API Key 精确计数（含停用 Key）。 */
  boundApiKeyCount: number
  /** 运行态累计防护次数（原始命中数口径，不去重；写后缓存按周期合并入库，内置行恒 0）。 */
  protectionCount: number
  updatedAt: string
}

/** 详情与列表行同型（低增长配置类资源，无独立 detail 投影）。 */
export type SecurityPolicyDetail = SecurityPolicyListItem

export interface SecurityPolicyOptionSummary {
  id: string
  name: string
  builtIn: boolean
  /** 运行态累计防护次数（写后缓存按周期合并入库，内置行恒 0）。 */
  protectionCount: number
  /** 管理面 options 返回 owner；个人面无 owner 字段。 */
  systemAccountId?: string
  systemAccountName?: string
}

export interface SecurityPolicyListResult {
  items: SecurityPolicyListItem[]
  total: number
  hasMore?: boolean
  page: number
  pageSize: number
}

export interface SecurityPolicyRowPatch {
  name?: string
  description?: string | null
  rules?: SecurityPolicyRule[]
}

export interface SecurityPolicyMutationResult {
  id: string
  changedFields: string[]
  rowPatch: SecurityPolicyRowPatch
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
  /**
   * 时间计划闸门（时间计划闸门与账户状态解耦设计 §3.1）：null=未配置计划（恒开），
   * 1=当前在允许时段内，0=当前不在允许时段内（仅暂停调用，不改写 status）。
   */
  availabilityScheduleGate?: number | null
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
  /** 绑定安全策略 ID；null/缺失 = 内置「默认安全策略」（空规则 = 原样转发）。 */
  securityPolicyId?: string | null
  securityPolicyName?: string
  securityPolicyBuiltIn?: boolean
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
  securityPolicyId?: string | null
  securityPolicyName?: string
  securityPolicyBuiltIn?: boolean
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

// 代理池（代理池设计 §4.1/§8.1）：管理面列表项与详情成员契约。
export type ProxyPoolStrategy = 'round_robin' | 'weighted'

export type ProxyPoolHealthStatus = 'healthy' | 'degraded' | 'dead' | 'unknown' | string

export interface ProxyPoolSummary {
  id: string
  name: string
  description?: string
  strategy: ProxyPoolStrategy
  enabled: boolean
  memberCount: number
  assignableMemberCount: number
  leaseCount: number
  updatedAt: string
}

export interface ProxyPoolListResult {
  items: ProxyPoolSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface ProxyPoolMemberDetail {
  proxyProfileId: string
  name: string
  type: string
  enabled: boolean
  testStatus: string
  lastTestedAt?: string
  latencyMs?: number
  poolHealthStatus: ProxyPoolHealthStatus
  poolHealthFailureStreak: number
  poolHealthCheckedAt?: string
  priority: number
  weight: number
  memberEnabled: boolean
  leaseCount: number
  assignable: boolean
}

export interface ProxyPoolDetail extends ProxyPoolSummary {
  members: ProxyPoolMemberDetail[]
}

/** 管理员成员选择器（§8.1 member-options）：不返回 host/port/用户名/密码。 */
export interface ProxyPoolMemberOptionSummary {
  id: string
  name: string
  type: string
  enabled: boolean
  testStatus: string
}

/** 账户下拉（§8.2）：kinds 显式请求 pool 时返回池摘要项，不含成员与敏感字段。 */
export interface ProxyPoolOptionSummary {
  id: string
  name: string
  kind: 'pool'
  enabled: boolean
  memberCount: number
  assignableMemberCount: number
}

export type ProxyOptionKind = 'profile' | 'pool'

/** /proxies/options 响应项（§8.2）：按 kind 判别；缺省调用方拿到 profile 项（kind 可缺省）。 */
export type ProxyOptionItem =
  | (ProxyProfileOptionSummary & { kind?: 'profile' })
  | ProxyPoolOptionSummary

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
