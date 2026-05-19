import type { AccountUsageStatsRange, AccountUsageSummary } from '../domain/types.js'
import type { ProcessRole } from '../config/runtime.js'

export const GLOBAL_STATS_SYSTEM_ACCOUNT_ID = 'global'
export const GLOBAL_STATS_SCOPE_ID = 'global'

export interface AccountUsageAggregateRow {
  account_id: string
  request_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_read_cost: number
  total_cost: number
  last_used_at: string | null
}

export interface UsageStatsRecordRow {
  id: string
  system_account_id: string
  trace_id: string
  client_ip: string | null
  api_key_id: string | null
  group_id: string | null
  account_id: string | null
  endpoint: string | null
  provider_code: string | null
  model: string | null
  status_code: number | null
  success: number
  first_token_ms: number | null
  duration_ms: number | null
  input_tokens: number | null
  output_tokens: number | null
  cache_read_tokens: number | null
  cache_read_cost_usd: number | null
  cost_usd: number | null
  error_code: string | null
  error_message: string | null
  account_owner_system_account_id: string | null
  group_owner_system_account_id: string | null
  account_access_type: string | null
  group_access_type: string | null
  account_authorization_id: string | null
  account_authorization_source_type: string | null
  account_authorization_source_team_id: string | null
  group_authorization_id: string | null
  group_authorization_source_type: string | null
  group_authorization_source_team_id: string | null
  created_at: string
}

export const USAGE_STATS_RECORD_SELECT_COLUMNS = `
  id,
  system_account_id,
  trace_id,
  client_ip,
  api_key_id,
  group_id,
  account_id,
  endpoint,
  provider_code,
  model,
  status_code,
  success,
  first_token_ms,
  duration_ms,
  input_tokens,
  output_tokens,
  cache_read_tokens,
  cache_read_cost_usd,
  cost_usd,
  error_code,
  error_message,
  account_owner_system_account_id,
  group_owner_system_account_id,
  account_access_type,
  group_access_type,
  account_authorization_id,
  account_authorization_source_type,
  account_authorization_source_team_id,
  group_authorization_id,
  group_authorization_source_type,
  group_authorization_source_team_id,
  created_at
`

export interface StatsJobStateRow {
  cursor_created_at: string | null
  cursor_id: string | null
  lag_seconds: number | null
}

export interface UsageStatsAccumulator {
  requestCount: number
  successCount: number
  errorCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheReadCostUsd: number
  totalCostUsd: number
  durationMsSum: number
  durationMsCount: number
  durationMsMax: number
  firstTokenMsSum: number
  firstTokenMsCount: number
  firstTokenMsMax: number
  lastUsedAt?: string
  lastErrorAt?: string
}

export interface UsageStatsEntry {
  systemAccountId: string
  scopeType: string
  scopeId: string
  accumulator: UsageStatsAccumulator
}

export interface StatsAggregateMathRow {
  request_count: number
  success_count: number
  error_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_read_cost_usd: number
  total_cost: number
  duration_ms_sum: number
  duration_ms_count: number
  duration_ms_max?: number
  first_token_ms_sum: number
  first_token_ms_count: number
  first_token_ms_max?: number
  last_used_at: string | null
  last_error_at?: string | null
}

export interface SystemMetricsSampleInput {
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

export interface ProcessEventLoopSampleInput {
  processRole: ProcessRole
  processPid?: number
  sampledAt?: string
  eventLoopLagMs?: number
}

export interface UsageStatsOverview {
  range: AccountUsageStatsRange
  summary: AccountUsageSummary & { successCount: number; errorCount: number; errorRate: number; averageDurationMs?: number; averageFirstTokenMs?: number }
  hourlyTrend: Array<{ statHour: string; requestCount: number; totalTokens: number; totalCost: number; averageDurationMs?: number; errorCount: number }>
  modelDistribution: Array<{ model: string; providerCode: string; requestCount: number; totalTokens: number; totalCost: number }>
  errors: Array<{ errorCode: string; providerCode: string; statusCode?: number; errorMessage?: string; errorCount: number }>
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
  processEventLoopLatest: Array<{
    processRole: ProcessRole
    processPid?: number
    sampledAt: string
    eventLoopLagMs?: number
  }>
  processEventLoopTrend: Array<{
    statHour: string
    processRole: ProcessRole
    sampleCount: number
    eventLoopLagMsAvg?: number
    eventLoopLagMsMax?: number
  }>
  backgroundJobs: Array<{
    name: string
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
  }>
}
