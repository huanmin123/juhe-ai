import type { AccountStatus, ProviderCode, ResourceAccessType } from './base'

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
  /** 媒体计量维度（M4a，usage_stats 聚合列）。 */
  inputAudioTokens: number
  outputAudioTokens: number
  ttsInputChars: number
  audioInputSeconds: number
  outputVideoSeconds: number
  totalTokens: number
  totalCost: number
  lastUsedAt?: string
}

export interface AccountUsageStatsRange {
  startDate: string
  endDate: string
  days: number
  maxDays: number
}

export interface UsageStatsWindow {
  timezone: string
  startDate: string
  endDate: string
  days: number
  maxDays: number
}

export interface AccountUsageDailyPoint extends AccountUsageSummary {
  statDate: string
}

export interface AccountUsageStatsTrendOverview {
  range: AccountUsageStatsRange
  rows: Array<{
    id: string
    name: string
    providerCode: ProviderCode
    systemAccountId?: string
    systemAccountName?: string
    ownerSystemAccountId: string
    ownerSystemAccountName?: string
    accessType?: ResourceAccessType
    dailyUsage: AccountUsageDailyPoint[]
  }>
}

export interface AccountUsageStatsSummaryResult {
  range: AccountUsageStatsRange
  summary: AccountUsageSummary
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
  providerCode: ProviderCode
  points: AiPerformancePoint[]
}

export interface AiPerformanceBaseResult {
  range: AccountUsageStatsRange
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

export interface AiPerformanceSeriesResult {
  range: AccountUsageStatsRange
  accounts: AiPerformanceAccount[]
  hourlySeries: AiPerformanceAccountSeries[]
}

export type AiPerformanceOverview = AiPerformanceBaseResult

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

export interface UsageStatsOverview {
  range: AccountUsageStatsRange
  summary: {
    requestCount: number
    successCount: number
    errorCount: number
    errorRate: number
    inputTokens: number
    outputTokens: number
    cacheReadTokens: number
    totalTokens: number
    totalCost: number
    averageDurationMs?: number
    averageFirstTokenMs?: number
  }
  hourlyTrend: Array<{
    statHour: string
    requestCount: number
    averageDurationMs?: number
    errorCount: number
  }>
  modelDistribution: Array<{
    model: string
    providerCode: string
    requestCount: number
    totalTokens: number
    totalCost: number
  }>
  errors: Array<{
    errorCode: string
    providerCode: string
    statusCode?: number
    errorMessage?: string
    errorCount: number
  }>
}

export interface UsageStatsOverviewSummaryResult {
  range: AccountUsageStatsRange
  summary: UsageStatsOverview['summary']
}

export interface UsageStatsOverviewDailyTrendResult {
  range: AccountUsageStatsRange
  dailyTrend: Array<{
    statDate: string
    totalTokens: number
    totalCost: number
  }>
}

export interface UsageStatsOverviewHourlyTrendResult {
  range: AccountUsageStatsRange
  hourlyTrend: UsageStatsOverview['hourlyTrend']
}

export interface UsageStatsOverviewModelDistributionResult {
  range: AccountUsageStatsRange
  modelDistribution: UsageStatsOverview['modelDistribution']
}

export interface UsageStatsOverviewErrorsResult {
  range: AccountUsageStatsRange
  errors: UsageStatsOverview['errors']
}
