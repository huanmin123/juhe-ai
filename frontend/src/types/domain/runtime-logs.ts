export type RuntimeLogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal'

export interface RuntimeLogSummary {
  id: string
  time: string
  level: RuntimeLogLevel | string
  traceId?: string
  event?: string
  message?: string
  errorMessage?: string
  rawJson?: string
  createdAt: string
}

export type RuntimeLogDetail = RuntimeLogSummary & { rawJson: string }

export interface RuntimeLogSearchResult {
  items: RuntimeLogSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
  elapsedMs: number
  retentionDays: number | null
  retentionDaysSource: 'worker_snapshot' | 'unavailable'
  runtimeAvailable: boolean
  workerSnapshotAvailable: boolean
  runtimeLogIndexQueueAvailable: boolean
}

export interface RuntimeLogGrepItem {
  id: string
  file: string
  fileName: string
  lineNumber?: number
  time: string
  level: RuntimeLogLevel | string
  traceId?: string
  event?: string
  message?: string
  errorMessage?: string
  rawJson: string
  line: string
}

export interface RuntimeLogGrepResult {
  available: boolean
  elapsedMs: number
  keywords: string[]
  startAt: string
  endAt: string
  defaultRangeDays: number
  maxRangeDays: number
  items: RuntimeLogGrepItem[]
  limit: number
  truncated: boolean
  scannedFileCount: number
  message?: string
}

export interface RuntimeLogIndexRuntime {
  queueLength: number
  droppedCount: number
  flushLastSuccessAt?: string
  flushLastError?: string
  retentionDays: number
}

export interface RuntimeLogGrepRuntime {
  earliestFileTime?: string
  defaultStartAt: string
  defaultEndAt: string
  defaultRangeDays: number
  maxRangeDays: number
  fileRetentionDays: number
  activeSearchCount: number
  maxConcurrentSearches: number
}

export type RuntimeLogQueueHealthStatus = 'normal' | 'backlogged' | 'degraded' | 'unavailable'

export interface RuntimeLogQueueHealthItem {
  key: string
  label: string
  source: 'worker_local' | 'server_ipc'
  status: RuntimeLogQueueHealthStatus
  reasons: string[]
  queueLength: number | null
  queueBytes: number | null
  droppedCount: number | null
  droppedOverflowCount: number | null
  droppedOversizeCount: number | null
  droppedSuccessCount: number | null
  droppedFailureCount: number | null
  rejectedCount: number | null
  flushFailureCount: number | null
  flushLastError?: string
  oldestQueuedMs: number | null
  lastFlushMs: number | null
  maxFlushMs: number | null
  slowFlushCount: number | null
  lastSlowFlushAt?: string
  writerPoolEnabled: boolean | null
  writerPoolWorkerCount: number | null
  writerPoolQueueLength: number | null
  writerPoolActiveJobs: number | null
  writerPoolHandledJobs: number | null
  writerPoolFailedJobs: number | null
  writerPoolRejectedJobs: number | null
  writerPoolOldestQueuedMs: number | null
  writerPoolMaxQueueWaitMs: number | null
  writerPoolMaxRunMs: number | null
  pendingWriteRequestCount: number | null
  oldestPendingWriteMs: number | null
}

export interface RuntimeLogQueueHealth {
  available: boolean
  workerSnapshotAvailable: boolean
  serverIpcQueueAvailable: boolean
  status: RuntimeLogQueueHealthStatus
  reasons: string[]
  summary: {
    degradedCount: number
    backloggedCount: number
    unavailableCount: number
    droppedCount: number
    rejectedCount: number
    flushFailureCount: number
    queuedCount: number
    queuedBytes: number
    pendingWriteRequestCount: number
    writerPoolQueuedCount: number
    writerPoolActiveJobs: number
  }
  workerQueues: RuntimeLogQueueHealthItem[]
  serverIpcQueues: RuntimeLogQueueHealthItem[]
}

export interface RuntimeLogFacets {
  retentionDays: number
  earliestIndexedAt?: string
  latestIndexedAt?: string
  totalIndexed: number
  levels: Array<{ value: string; count: number }>
  events: string[]
  runtimeAvailable: boolean
  workerSnapshotAvailable: boolean
  runtimeLogIndexQueueAvailable: boolean
  runtime: RuntimeLogIndexRuntime | null
  worker: {
    available: boolean
    snapshotAvailable: boolean
    pid?: number
    ready: boolean | null
    pendingMessageCount: number | null
  }
  dbService: {
    statusAvailable: boolean
    stateAvailable: boolean
    pid?: number
    ready: boolean | null
    pendingRequestCount: number | null
    pendingDatasetWriteRequestCount?: number
    oldestDatasetWriteRequestMs?: number
    timedOutRequestCount: number | null
    failedRequestCount: number | null
    queuedRequestCount?: number
    queuedHighRequestCount?: number
    queuedNormalRequestCount?: number
    queuedLowRequestCount?: number
    oldestQueuedMs?: number
    lastQueueWaitMs?: number
    maxQueueWaitMs?: number
    lastExecMs?: number
    maxExecMs?: number
    slowOpCount?: number
    lastSlowOpType?: string
    lastSlowOpMs?: number
    lastSlowOpAt?: string
    unavailableCircuitOpenUntil?: string
    httpHost?: string
    httpPort?: number
    handledRequestCount?: number
    lastRequestAt?: string
    lastError?: string
  }
  queueHealth: RuntimeLogQueueHealth
  grep: RuntimeLogGrepRuntime
  gatewayAccountSideEffectsAvailable: boolean
  gatewayAccountSideEffects: {
    queueLength: number
    processing: boolean
    enqueuedCount: number
    completedCount: number
    failedAttemptCount: number
    droppedCount: number
    expiredCount: number
    localSuppressedAccountCount: number
    nextAttemptAt?: string
  } | null
}
