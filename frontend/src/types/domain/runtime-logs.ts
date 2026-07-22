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
  discoveredFileCount: number
  pendingFileCount: number
  pendingBytes: number
  oldestPendingMtime?: string
  currentFile?: string
  currentOffset: number
  lastReadAt?: string
  lastCommitAt?: string
  lastError?: string
  protectedRotatedFileCount: number
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
  indexEnabled?: boolean
  unavailableReason?: string
  runtimeAvailable: boolean
  workerSnapshotAvailable: boolean
  runtimeLogIndexQueueAvailable: boolean
  runtime: { lastError?: string } | null
  worker: {
    ready: boolean | null
    pendingMessageCount: number | null
  }
  queueHealth: {
    available: boolean
    workerSnapshotAvailable: boolean
    serverIpcQueueAvailable: boolean
    status: string
    summary: {
      unavailableCount: number
      droppedCount: number
      rejectedCount: number
    }
  }
  dbService: {
    statusAvailable: boolean
    stateAvailable: boolean
    ready: boolean | null
  }
  gatewayAccountSideEffectsAvailable: boolean
  gatewayAccountSideEffects: unknown
}

export interface RuntimeLogRuntime {
  runtimeAvailable: boolean
  ingestWorkerAvailable: boolean
  runtimeLogIndexQueueAvailable: boolean
  dbService: {
    statusAvailable: boolean
    stateAvailable: boolean
  }
  gatewayAccountSideEffectsAvailable: boolean
}
