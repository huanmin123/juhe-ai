import type { AccountStatus, ProcessRole, ProviderCode, ResourceAccessType } from './base'

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
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface UsageStatsOverview {
  range: AccountUsageStatsRange
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
    cacheReadTokens?: number
    cacheWriteTokens?: number
    cacheWrite1hTokens?: number
    cacheWriteCost?: number
    thinkingTokens?: number
    inputImageTokens?: number
    outputImageTokens?: number
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
    cacheReadTokens?: number
    cacheWriteTokens?: number
    cacheWrite1hTokens?: number
    cacheWriteCost?: number
    thinkingTokens?: number
    inputImageTokens?: number
    outputImageTokens?: number
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

export interface SystemMetricsOverview {
  hourlyTrend: Array<{
    statHour: string
    sampleCount: number
    cpuPercentAvg?: number
    cpuPercentMax?: number
    memoryUsedPercentAvg?: number
    memoryUsedPercentMax?: number
    eventLoopLagMsSampleCount?: number
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
  processEventLoopLatestStatus: Array<{
    processRole: ProcessRole
    sampleAvailable: boolean
    processPid: number | null
    sampledAt: string | null
    eventLoopLagMs: number | null
    processRssBytes: number | null
    processHeapUsedBytes: number | null
    processHeapTotalBytes: number | null
    processExternalBytes: number | null
    processArrayBuffersBytes: number | null
  }>
  processEventLoopPeakStatus: Array<{
    processRole: ProcessRole
    sampleAvailable: boolean
    processPid: number | null
    sampledAt: string | null
    eventLoopLagMs: number | null
    processRssBytes: number | null
    processHeapUsedBytes: number | null
    processHeapTotalBytes: number | null
    processExternalBytes: number | null
    processArrayBuffersBytes: number | null
  }>
  processEventLoopTrend: Array<{
    statHour: string
    statMinute: string
    processRole: ProcessRole
    sampleCount: number
    eventLoopLagMsSampleCount?: number
    eventLoopLagMsAvg?: number
    eventLoopLagMsMax?: number
    processRssBytesAvg?: number
    processRssBytesMax?: number
    processHeapUsedBytesAvg?: number
    processHeapUsedBytesMax?: number
    processHeapTotalBytesAvg?: number
    processHeapTotalBytesMax?: number
  }>
}

export interface SystemMetricsTrendOverview {
  hourlyTrend: Array<Pick<SystemMetricsOverview['hourlyTrend'][number],
    'statHour' | 'cpuPercentAvg' | 'memoryUsedPercentAvg' | 'networkRxBytesPerSecondAvg' | 'networkTxBytesPerSecondAvg'
  >>
  processEventLoopLatestStatus: Array<Pick<SystemMetricsOverview['processEventLoopLatestStatus'][number],
    'processRole' | 'sampleAvailable' | 'processPid' | 'sampledAt' | 'eventLoopLagMs' | 'processRssBytes' | 'processHeapUsedBytes' | 'processHeapTotalBytes'
  >>
  processEventLoopPeakStatus: Array<Pick<SystemMetricsOverview['processEventLoopPeakStatus'][number],
    'processRole' | 'sampleAvailable' | 'processPid' | 'sampledAt' | 'eventLoopLagMs'
  >>
  processEventLoopTrend: Array<Pick<SystemMetricsOverview['processEventLoopTrend'][number],
    'statMinute' | 'processRole' | 'eventLoopLagMsAvg' | 'eventLoopLagMsMax' | 'processRssBytesAvg' | 'processRssBytesMax'
  >>
}

export interface SystemMetricsRuntimeOverview {
  runtimeSnapshotAvailable: boolean
  runtimeSnapshotStale?: boolean
  ingestWorkerSnapshotAvailable?: boolean
  statsWorkerSnapshotAvailable?: boolean
  opsWorkerSnapshotAvailable?: boolean
  backgroundJobsAvailable: boolean
  backgroundJobs: Array<{
    name: string
    workerRole?: ProcessRole
    intervalMs: number
    resourceLane?: string
    running: boolean
    pending?: boolean
    queuedForLane?: boolean
    timedOut?: boolean
    nextRunAt?: string
    lastStartedAt?: string
    lastFinishedAt?: string
    lastSuccessAt?: string
    lastErrorAt?: string
    lastError?: string
    lastWarningAt?: string
    lastWarning?: string
    lastOutcome?: 'success' | 'partial' | 'failure' | 'timeout' | 'skipped'
    leaseState?: 'not_required' | 'acquired' | 'busy' | 'lost'
    lastDurationMs?: number
    maxDurationMs?: number
    runCount: number
    successCount: number
    failureCount: number
    partialCount: number
    skippedCount: number
    taskSkippedCount?: number
    coalescedCount?: number
    timedOutCount?: number
    retryQueue?: {
      name: string
      pendingCount: number
      runningCount: number
      nextRunAt?: string
    }
    localQueue?: {
      name: string
      queueType?: string
      queueLength?: number
      queueBytes?: number
      flushLastSuccessAt?: string
      flushLastError?: string
      completedCount?: number
      droppedCount?: number
      rejectedCount?: number
      expiredCount?: number
      timedOutCount?: number
      failedCount?: number
      flushFailureCount?: number
      oldestQueuedMs?: number
      writerPoolQueueLength?: number
      writerPoolActiveJobs?: number
      writerPoolFailedJobs?: number
      writerPoolRejectedJobs?: number
      writerPoolOldestQueuedMs?: number
      pendingWriteRequestCount?: number
      pendingWriteOldestQueuedMs?: number
      runningCount?: number
      consumers?: number
      nextRunAt?: string
    }
  }> | null
}
