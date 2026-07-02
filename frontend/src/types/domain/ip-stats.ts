import type { AccountUsageStatsRange } from './usage-stats'

export type ClientIpPolicyStatus = 'active' | 'disabled'
export type ClientIpStatus = 'all' | 'normal' | 'blacklisted'
export type ClientIpStatsSortField = 'requestCount' | 'successCount' | 'errorCount' | 'errorRate' | 'totalTokens' | 'totalCost' | 'activeDays' | 'lastUsedAt'

export interface ClientIpUsageSummary {
  requestCount: number
  successCount: number
  errorCount: number
  errorRate: number
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
  activeDays: number
  averageDurationMs?: number
  averageFirstTokenMs?: number
  maxDurationMs?: number
  lastUsedAt?: string
  lastErrorAt?: string
}

export interface ClientIpStatsRow {
  ipHash: string
  aggregateIpKey: string
  lastSeenAt?: string
  status: ClientIpStatus
  rangeUsage: ClientIpUsageSummary
}

export interface ClientIpStatsListResult {
  items: ClientIpStatsRow[]
  pageUpperBound: number
  hasMore: boolean
  page: number
  pageSize: number
  range: AccountUsageStatsRange
  rangeReady: boolean
}

export interface ClientIpAccountUsageRow {
  accountId: string
  accountName?: string
  accountOwnerSystemAccountId?: string
  accountOwnerSystemAccountName?: string
  rangeUsage: ClientIpUsageSummary
}

export interface ClientIpStatsDetailResult {
  ipHash: string
  aggregateIpKey: string
  lastSeenAt?: string
  items: ClientIpAccountUsageRow[]
  pageUpperBound: number
  hasMore: boolean
  page: number
  pageSize: number
  range: AccountUsageStatsRange
  rangeReady: boolean
}

export interface ClientIpPolicySummary {
  id: string
  ipHash: string
  status: ClientIpPolicyStatus
  reason?: string
  expiresAt?: string
  createdBySystemAccountId: string
  createdAt: string
  updatedAt: string
  disabledAt?: string
  disabledBySystemAccountId?: string
  disabledReason?: string
}
