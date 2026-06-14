import type { ProcessEventLoopRole } from '../shared/process-event-loop-monitor.js'
import type { AccountUsageSummary } from '../domain/types.js'
import { averageFromSum, numberFromUnknown, usageSummaryFromAggregate } from './usage-stats-helpers.js'
import type { AccountUsageAggregateRow, StatsAggregateMathRow, SystemMetricsOverview } from './usage-stats-types.js'

export function usageSummaryWithMath(row: AccountUsageAggregateRow & StatsAggregateMathRow): AccountUsageSummary & { successCount: number; errorCount: number; errorRate: number; averageDurationMs?: number; averageFirstTokenMs?: number } {
  const summary = usageSummaryFromAggregate(row)
  const successCount = Number(row.success_count ?? 0)
  const errorCount = Number(row.error_count ?? 0)
  const requestCount = Number(row.request_count ?? 0)
  return {
    ...summary,
    successCount,
    errorCount,
    errorRate: requestCount > 0 ? errorCount / requestCount : 0,
    averageDurationMs: averageFromSum(row.duration_ms_sum, row.duration_ms_count),
    averageFirstTokenMs: averageFromSum(row.first_token_ms_sum, row.first_token_ms_count)
  }
}

export function emptyStatsAggregateMathRow(): AccountUsageAggregateRow & StatsAggregateMathRow {
  return {
    account_id: '',
    request_count: 0,
    success_count: 0,
    error_count: 0,
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_read_cost_usd: 0,
    total_cost: 0,
    duration_ms_sum: 0,
    duration_ms_count: 0,
    first_token_ms_sum: 0,
    first_token_ms_count: 0,
    last_used_at: null
  }
}

export function mapSystemMetricsLatest(row: Record<string, unknown>): SystemMetricsOverview['latest'] {
  return {
    sampledAt: String(row.sampled_at),
    cpuPercent: numberFromUnknown(row.cpu_percent),
    memoryUsedPercent: numberFromUnknown(row.memory_used_percent),
    memoryTotalBytes: numberFromUnknown(row.memory_total_bytes),
    memoryFreeBytes: numberFromUnknown(row.memory_free_bytes),
    processRssBytes: numberFromUnknown(row.process_rss_bytes),
    processHeapUsedBytes: numberFromUnknown(row.process_heap_used_bytes),
    processHeapTotalBytes: numberFromUnknown(row.process_heap_total_bytes),
    eventLoopLagMs: numberFromUnknown(row.event_loop_lag_ms),
    networkRxBytesPerSecond: numberFromUnknown(row.network_rx_bytes_per_sec),
    networkTxBytesPerSecond: numberFromUnknown(row.network_tx_bytes_per_sec),
    networkRxTotalBytes: numberFromUnknown(row.network_rx_total_bytes),
    networkTxTotalBytes: numberFromUnknown(row.network_tx_total_bytes),
    dbFileBytes: numberFromUnknown(row.db_file_bytes),
    statsLagSeconds: numberFromUnknown(row.stats_lag_seconds)
  }
}

export function mapSystemMetricsHourly(row: Record<string, unknown>): SystemMetricsOverview['hourlyTrend'][number] {
  const sampleCount = Number(row.sample_count ?? 0)
  const eventLoopLagMsSampleCount = Number(row.event_loop_lag_ms_count ?? 0)
  return {
    statHour: String(row.stat_hour),
    sampleCount,
    cpuPercentAvg: averageFromSum(row.cpu_percent_sum, sampleCount),
    cpuPercentMax: numberFromUnknown(row.cpu_percent_max),
    memoryUsedPercentAvg: averageFromSum(row.memory_used_percent_sum, sampleCount),
    memoryUsedPercentMax: numberFromUnknown(row.memory_used_percent_max),
    eventLoopLagMsSampleCount,
    eventLoopLagMsAvg: averageFromSum(row.event_loop_lag_ms_sum, eventLoopLagMsSampleCount),
    eventLoopLagMsMax: numberFromUnknown(row.event_loop_lag_ms_max),
    networkRxBytesPerSecondAvg: averageFromSum(row.network_rx_bytes_per_sec_sum, row.network_rx_bytes_per_sec_count),
    networkRxBytesPerSecondMax: numberFromUnknown(row.network_rx_bytes_per_sec_max),
    networkTxBytesPerSecondAvg: averageFromSum(row.network_tx_bytes_per_sec_sum, row.network_tx_bytes_per_sec_count),
    networkTxBytesPerSecondMax: numberFromUnknown(row.network_tx_bytes_per_sec_max),
    networkRxTotalBytesMax: numberFromUnknown(row.network_rx_total_bytes_max),
    networkTxTotalBytesMax: numberFromUnknown(row.network_tx_total_bytes_max),
    processRssBytesMax: numberFromUnknown(row.process_rss_bytes_max),
    processHeapUsedBytesMax: numberFromUnknown(row.process_heap_used_bytes_max),
    dbFileBytesMax: numberFromUnknown(row.db_file_bytes_max),
    statsLagSecondsMax: numberFromUnknown(row.stats_lag_seconds_max)
  }
}

export function mapProcessEventLoopHourly(row: Record<string, unknown>): SystemMetricsOverview['processEventLoopTrend'][number] {
  const processRole = processRoleFromUnknown(row.process_role)
  if (typeof row.stat_hour !== 'string' || !row.stat_hour) {
    throw new Error('进程事件循环统计缺少 stat_hour')
  }
  const statBucket = row.stat_hour
  return {
    statHour: statBucket,
    statMinute: statBucket,
    processRole,
    sampleCount: Number(row.sample_count ?? 0),
    eventLoopLagMsAvg: averageFromSum(row.event_loop_lag_ms_sum, row.sample_count),
    eventLoopLagMsMax: numberFromUnknown(row.event_loop_lag_ms_max)
  }
}

function processRoleFromUnknown(value: unknown): ProcessEventLoopRole {
  if (value === 'server' || value === 'worker' || value === 'metrics-worker' || value === 'ingest-worker' || value === 'temporary-maintenance-worker' || value === 'db-service') {
    return value
  }
  throw new Error(`非法进程角色：${String(value)}`)
}

