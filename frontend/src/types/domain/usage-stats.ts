import type { AccountStatus, ProcessRole, ProviderCode, ResourceAccessType } from './base'

export interface AccountUsageSummary {
  requestCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheReadCost: number
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

export interface AccountUsageDailyPoint extends AccountUsageSummary {
  statDate: string
}

export interface AiPerformanceAccount {
  id: string
  name: string
  status: AccountStatus
  providerCode: ProviderCode
  systemAccountId: string
  systemAccountName?: string
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  accessType?: ResourceAccessType
  requestCountLast7d: number
  selected: boolean
  defaultVisible: boolean
}

export interface AiPerformanceAccountOption {
  id: string
  name: string
  status: AccountStatus
  providerCode: ProviderCode
  systemAccountId: string
  systemAccountName?: string
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  accessType?: ResourceAccessType
  requestCountLast7d: number
}

export interface AiPerformancePoint {
  statHour: string
  requestCount: number
  firstTokenCount: number
  averageFirstTokenMs?: number
  maxFirstTokenMs?: number
  durationCount: number
  averageDurationMs?: number
  maxDurationMs?: number
}

export interface AiPerformanceAccountSeries {
  accountId: string
  accountName: string
  providerCode: ProviderCode
  systemAccountId: string
  points: AiPerformancePoint[]
}

export interface AiPerformanceOverview {
  range: AccountUsageStatsRange
  defaultAccounts: AiPerformanceAccount[]
  selectedAccounts: AiPerformanceAccount[]
  accounts: AiPerformanceAccount[]
  hourlySeries: AiPerformanceAccountSeries[]
  summary: {
    requestCount: number
    firstTokenCount: number
    averageFirstTokenMs?: number
    maxFirstTokenMs?: number
    durationCount: number
    averageDurationMs?: number
    maxDurationMs?: number
  }
  statsLagSeconds?: number
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
  statsLagSeconds?: number
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
  runtimeSnapshotAvailable: boolean
  ingestWorkerSnapshotAvailable?: boolean
  statsWorkerSnapshotAvailable?: boolean
  opsWorkerSnapshotAvailable?: boolean
  ingestWorker?: {
    pid: number | null
    ready: boolean
    snapshotAvailable: boolean
  } | null
  statsWorker?: {
    pid: number | null
    ready: boolean
    snapshotAvailable: boolean
  } | null
  opsWorker?: {
    pid: number | null
    ready: boolean
    snapshotAvailable: boolean
  } | null
  backgroundJobsAvailable: boolean
  backgroundJobs: Array<{
    name: string
    workerRole?: ProcessRole
    intervalMs: number
    running: boolean
    lastStartedAt?: string
    lastFinishedAt?: string
    lastSuccessAt?: string
    lastErrorAt?: string
    lastError?: string
    lastDurationMs?: number
    maxDurationMs?: number
    runCount: number
    successCount: number
    failureCount: number
    skippedCount: number
    retryQueue?: {
      name: string
      pendingCount: number
      runningCount: number
      nextRunAt?: string
    }
    localQueue?: {
      name: string
      queueLength?: number
      queueBytes?: number
      flushLastSuccessAt?: string
      flushLastError?: string
      completedCount?: number
      droppedCount?: number
      droppedSuccessCount?: number
      droppedFailureCount?: number
      droppedOverflowCount?: number
      droppedOversizeCount?: number
      retainedOverflowWarningCount?: number
      flushFailureCount?: number
      [key: string]: unknown
    }
  }> | null
}
