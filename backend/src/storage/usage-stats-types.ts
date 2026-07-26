import type { AccountUsageStatsRange, AccountUsageSummary } from '../domain/types.js'
import type { ProcessEventLoopRole } from '../shared/process-event-loop-monitor.js'

export const GLOBAL_STATS_SYSTEM_ACCOUNT_ID = 'global'
export const GLOBAL_STATS_SCOPE_ID = 'global'

export interface AccountUsageAggregateRow {
  account_id: string
  request_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_read_cost_usd: number
  cache_write_tokens?: number
  cache_write_1h_tokens?: number
  cache_write_cost_usd?: number
  thinking_tokens?: number
  input_image_tokens?: number
  output_image_tokens?: number
  total_cost: number
  last_used_at: string | null
}

export interface UsageStatsRecordRow {
  id: string
  system_account_id: string
  trace_id: string
  traffic_source: string
  client_ip: string | null
  api_key_id: string | null
  group_id: string | null
  account_id: string | null
  endpoint: string | null
  provider_code: string | null
  provider_protocol_profile_id?: string | null
  model: string | null
  status_code: number | null
  success: number
  failure_attribution: string | null
  first_token_ms: number | null
  duration_ms: number | null
  input_tokens: number | null
  output_tokens: number | null
  cache_read_tokens: number | null
  cache_read_cost_usd: number | null
  cache_write_tokens: number | null
  cache_write_1h_tokens: number | null
  cache_write_cost_usd: number | null
  thinking_tokens: number | null
  input_image_tokens: number | null
  output_image_tokens: number | null
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
  source_shard_key?: string | null
}

export const USAGE_STATS_RECORD_SELECT_COLUMNS = `
  id,
  system_account_id,
  trace_id,
  traffic_source,
  client_ip,
  api_key_id,
  group_id,
  account_id,
  endpoint,
  provider_code,
  provider_protocol_profile_id,
  model,
  status_code,
  success,
  failure_attribution,
  first_token_ms,
  duration_ms,
  input_tokens,
  output_tokens,
  cache_read_tokens,
  cache_read_cost_usd,
  cache_write_tokens,
  cache_write_1h_tokens,
  cache_write_cost_usd,
  thinking_tokens,
  input_image_tokens,
  output_image_tokens,
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
  cacheWriteTokens: number
  cacheWrite1hTokens: number
  cacheWriteCostUsd: number
  thinkingTokens: number
  inputImageTokens: number
  outputImageTokens: number
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
  cache_write_tokens?: number
  cache_write_1h_tokens?: number
  cache_write_cost_usd?: number
  thinking_tokens?: number
  input_image_tokens?: number
  output_image_tokens?: number
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
  sampledAt?: string
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
  processRole: ProcessEventLoopRole
  processPid?: number
  sampledAt?: string
  eventLoopLagMs?: number
  processRssBytes?: number
  processHeapUsedBytes?: number
  processHeapTotalBytes?: number
  processExternalBytes?: number
  processArrayBuffersBytes?: number
}

export interface UsageStatsOverview {
  range: AccountUsageStatsRange
  summary: AccountUsageSummary & { successCount: number; errorCount: number; errorRate: number; averageDurationMs?: number; averageFirstTokenMs?: number }
  hourlyTrend: Array<{ statHour: string; requestCount: number; totalTokens: number; cacheReadTokens?: number; cacheWriteTokens?: number; cacheWrite1hTokens?: number; cacheWriteCost?: number; thinkingTokens?: number; inputImageTokens?: number; outputImageTokens?: number; totalCost: number; averageDurationMs?: number; errorCount: number }>
  modelDistribution: Array<{ model: string; providerCode: string; requestCount: number; totalTokens: number; cacheReadTokens?: number; cacheWriteTokens?: number; cacheWrite1hTokens?: number; cacheWriteCost?: number; thinkingTokens?: number; inputImageTokens?: number; outputImageTokens?: number; totalCost: number }>
  errors: Array<{ errorCode: string; providerCode: string; statusCode?: number; errorMessage?: string; errorCount: number }>
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
    processRole: ProcessEventLoopRole
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
    processRole: ProcessEventLoopRole
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
    processRole: ProcessEventLoopRole
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
