export type RuntimeLogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal'

export interface RuntimeLogSummary {
  id: string
  time: string
  level: RuntimeLogLevel | string
  traceId?: string
  event?: string
  message?: string
  errorMessage?: string
  rawJson: string
  createdAt: string
}

export interface RuntimeLogSearchResult {
  items: RuntimeLogSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
  elapsedMs: number
  retentionDays: number
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
}

export interface RuntimeLogFacets {
  retentionDays: number
  earliestIndexedAt?: string
  latestIndexedAt?: string
  totalIndexed: number
  levels: Array<{ value: string; count: number }>
  events: string[]
  runtime: RuntimeLogIndexRuntime
  grep: RuntimeLogGrepRuntime
}
