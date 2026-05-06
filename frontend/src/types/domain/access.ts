import type { AccountUsageSummary } from './usage-stats'

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
  quotaLimits: ApiKeyQuotaLimits
  usage: AccountUsageSummary
}

export interface CreatedApiKey extends ApiKeySummary {}

export interface ApiKeyQuotaLimit {
  enabled: boolean
  limit: number
}

export interface ApiKeyHourlyQuotaLimit extends ApiKeyQuotaLimit {
  hours: number
}

export interface ApiKeyQuotaLimits {
  hourly?: ApiKeyHourlyQuotaLimit
  daily?: ApiKeyQuotaLimit
  weekly?: ApiKeyQuotaLimit
  monthly?: ApiKeyQuotaLimit
  total?: ApiKeyQuotaLimit
}

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
