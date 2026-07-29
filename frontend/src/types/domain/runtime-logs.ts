export type RuntimeLogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal'

export interface RuntimeLogSummary {
  id: string
  time: string
  level: RuntimeLogLevel | string
  traceId?: string
  event?: string
  message?: string
  errorMessage?: string
}

export interface RuntimeLogDetailDelta {
  id: string
  rawJson: string
}

export type RuntimeLogDetail = RuntimeLogSummary & RuntimeLogDetailDelta
export type RuntimeLogDetailView = RuntimeLogSummary & Partial<RuntimeLogDetailDelta>
export type RuntimeLogGrepDetailView = RuntimeLogGrepItem & Partial<RuntimeLogGrepDetail>

export interface RuntimeLogSearchResult {
  items: RuntimeLogSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface RuntimeLogGrepItem {
  id: string
  fileName: string
  lineNumber: number
  time: string
  level: RuntimeLogLevel | string
  traceId?: string
  event?: string
  message?: string
  errorMessage?: string
}

export interface RuntimeLogGrepDetail {
  file: string
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

export interface RuntimeLogFacets {
  retentionDays: number
  earliestIndexedAt?: string
  latestIndexedAt?: string
  totalIndexed: number
  levels: Array<{ value: string; count: number }>
  events: string[]
}
