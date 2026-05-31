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

export type ApiKeyGroupBindingStatus = 'active' | 'disabled'
export type ApiKeyGroupRouteStrategy = 'priority_failover' | 'round_robin' | 'weighted_round_robin'
export type ApiKeyAvailabilityScheduleMode = 'allow_windows'
export type ApiKeyAvailabilityScheduleExceptionAction = 'allow' | 'deny'

export interface ApiKeyGroupBindingSummary {
  id: string
  groupId: string
  groupName?: string
  providerCode?: string
  priority: number
  weight: number
  status: ApiKeyGroupBindingStatus
  groupEnabled: boolean
}

export interface ApiKeyAvailabilityScheduleWindow {
  daysOfWeek: number[]
  start: string
  end: string
}

export interface ApiKeyAvailabilityScheduleException {
  date: string
  action: ApiKeyAvailabilityScheduleExceptionAction
  windows?: Array<Pick<ApiKeyAvailabilityScheduleWindow, 'start' | 'end'>>
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
  key: string
  status: 'active' | 'disabled'
  groupRouteStrategy: ApiKeyGroupRouteStrategy
  groupBindings: ApiKeyGroupBindingSummary[]
  groupOwnerSystemAccountName?: string
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
