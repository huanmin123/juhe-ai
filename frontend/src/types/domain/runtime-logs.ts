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
  lineNumberFromEnd: number
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
  mode?: 'rg'
  elapsedMs: number
  keywords: string[]
  items: RuntimeLogGrepItem[]
  limit: number
  truncated: boolean
  scannedFileCount: number
  message?: string
  installSteps?: string[]
}

export interface RuntimeLogIndexRuntime {
  queueLength: number
  droppedCount: number
  flushLastSuccessAt?: string
  flushLastError?: string
  retentionDays: number
}

export interface RuntimeLogFacets {
  retentionDays: number
  earliestIndexedAt?: string
  latestIndexedAt?: string
  totalIndexed: number
  levels: Array<{ value: string; count: number }>
  events: string[]
  runtime: RuntimeLogIndexRuntime
}
