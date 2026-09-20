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

export interface GoRuntimeTrendItem {
  windowStart: string
  windowEnd: string
  service: string
  role: string
  runtimeKind: 'go'
  sampleCount: number
  goroutinesAvg: number
  goroutinesMax: number
  heapAllocBytesAvg: number
  heapAllocBytesMax: number
  heapLiveBytesAvg: number
  heapLiveBytesMax: number
  heapObjectsAvg: number
  heapObjectsMax: number
  threadsAvg: number
  threadsMax: number
  // Optional fields are present only after the Go metrics schema upgrade.
  // The UI must render an unavailable state when an older window omits them.
  cpuPercentAvg?: number | null
  cpuPercentMax?: number | null
  rssBytesAvg?: number | null
  rssBytesMax?: number | null
  fdCountAvg?: number | null
  fdCountMax?: number | null
  uptimeSecondsAvg?: number | null
  uptimeSecondsMax?: number | null
  goroutinesRunnableAvg?: number | null
  goroutinesRunnableMax?: number | null
  goroutinesWaitingAvg?: number | null
  goroutinesWaitingMax?: number | null
  gomaxprocsAvg?: number | null
  gomaxprocsMax?: number | null
  gcPauseP95SecondsAvg?: number | null
  gcPauseP95SecondsMax?: number | null
  gcPauseP99SecondsAvg?: number | null
  gcPauseP99SecondsMax?: number | null
  schedulerLatencyP95SecondsAvg?: number | null
  schedulerLatencyP95SecondsMax?: number | null
  schedulerLatencyP99SecondsAvg?: number | null
  schedulerLatencyP99SecondsMax?: number | null
}

export interface GoRuntimeTrendOverview {
  runtimeKind: 'go'
  service: string
  role: string
  timezone: string
  range: AccountUsageStatsRange
  items: GoRuntimeTrendItem[]
}

export interface SystemMetricsRuntimeJob {
  runId: string
  jobName: string
  jobType: string
  workerRole: string
  status: string
  startedAt: string | null
  finishedAt: string | null
  durationMs: number | null
  errorMessage: string | null
}

export interface SystemMetricsRuntimeJobsResult {
  items: SystemMetricsRuntimeJob[]
  total: number
  page: number
  pageSize: number
  hasMore: boolean
}

export interface SystemMetricsHealthSnapshot {
  checkedAt: string
  gateway: Record<string, unknown>
  jobs: {
    available: boolean
    reason?: string
    payload?: Record<string, unknown>
  }
}
