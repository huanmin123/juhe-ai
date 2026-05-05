export interface AccountUsageSummary {
  requestCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  totalTokens: number
  totalCost: number
  lastUsedAt?: string
}

export type UsageStatsWindowKey = 'last1d' | 'last3d' | 'last7d' | 'last15d' | 'last30d' | 'total'

export interface UsageStatsWindowDefinition {
  key: UsageStatsWindowKey
  label: string
  days?: number
}

export type UsageByWindow = Record<UsageStatsWindowKey, AccountUsageSummary>

export type UsageOverviewWindowKey = 'last1d' | 'last3d' | 'last7d' | 'last30d'

export interface UsageOverviewWindowDefinition {
  key: UsageOverviewWindowKey
  label: string
  hours: number
}

export interface UsageStatsOverview {
  window: UsageOverviewWindowDefinition
  summary: AccountUsageSummary & {
    successCount: number
    errorCount: number
    errorRate: number
    averageDurationMs?: number
    averageFirstTokenMs?: number
  }
  hourlyTrend: Array<{
    statHour: string
    requestCount: number
    totalTokens: number
    totalCost: number
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
  statsLagSeconds: number
}

export interface SystemMetricsOverview {
  latest?: {
    sampledAt: string
    cpuPercent?: number
    memoryUsedPercent?: number
    memoryTotalBytes?: number
    memoryFreeBytes?: number
    processRssBytes?: number
    processHeapUsedBytes?: number
    processHeapTotalBytes?: number
    eventLoopLagMs?: number
    networkRxBytesPerSecond?: number
    networkTxBytesPerSecond?: number
    networkRxTotalBytes?: number
    networkTxTotalBytes?: number
    dbFileBytes?: number
    statsLagSeconds?: number
  }
  hourlyTrend: Array<{
    statHour: string
    sampleCount: number
    cpuPercentAvg?: number
    cpuPercentMax?: number
    memoryUsedPercentAvg?: number
    memoryUsedPercentMax?: number
    eventLoopLagMsAvg?: number
    eventLoopLagMsMax?: number
    networkRxBytesPerSecondAvg?: number
    networkRxBytesPerSecondMax?: number
    networkTxBytesPerSecondAvg?: number
    networkTxBytesPerSecondMax?: number
    networkRxTotalBytesMax?: number
    networkTxTotalBytesMax?: number
    processRssBytesMax?: number
    processHeapUsedBytesMax?: number
    dbFileBytesMax?: number
    statsLagSecondsMax?: number
  }>
}
