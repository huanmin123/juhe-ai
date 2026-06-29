import type { AccountUsageSummary } from './usage-stats'

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

export interface RouteStrategyGroupBindingSummary {
  id: string
  groupId: string
  groupName?: string
  providerCode?: string
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  priority: number
  weight: number
  status: RouteStrategyGroupBindingStatus
  groupEnabled: boolean
}

export type RouteStrategyMode = 'normal' | 'round_robin' | 'weighted' | 'failover' | 'hybrid_smart'
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
  hybridRoutingConfig?: ApiKeyHybridRoutingConfig
  groupBindings: RouteStrategyGroupBindingSummary[]
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

export interface RouteStrategyListResult {
  items: RouteStrategySummary[]
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

export interface ApiKeyListResult {
  items: ApiKeySummary[]
  total: number
  hasMore?: boolean
  page: number
  pageSize: number
}

export interface CreatedApiKey extends ApiKeySummary {}

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
}

export type ProxyProfileOptionSummary = Pick<ProxyProfileSummary, 'id' | 'name' | 'type' | 'enabled'>

export interface ProxyProfileListResult {
  items: ProxyProfileSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export type ProxyTestItemStatus = 'passed' | 'warning' | 'failed'
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
