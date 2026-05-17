import type { AccountUsageStatsRange } from '../domain/types.js'
import { compareText, rowsForDateRange, trendBucketHours, trendBucketKey } from './usage-stats-window-helpers.js'

export interface UsageWindowAggregate {
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
}

export interface UsageStatsDailyWindowRow {
  stat_date: string
  request_count: number
  success_count: number
  error_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_read_cost_usd: number
  total_cost_usd: number
  duration_ms_sum: number
  duration_ms_count: number
  duration_ms_max: number
  first_token_ms_sum: number
  first_token_ms_count: number
  first_token_ms_max: number
  last_used_at: string | null
}

export interface UsageOverviewHourlyWindowRow {
  stat_hour: string
  request_count: number
  error_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_read_cost_usd: number
  total_cost_usd: number
  duration_ms_sum: number
  duration_ms_count: number
}

export interface UsageModelWindowRow {
  stat_date: string
  provider_code: string
  model: string
  request_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_read_cost_usd: number
  total_cost_usd: number
}

export interface UsageErrorWindowRow {
  stat_date: string
  error_group: string
  provider_code: string
  error_code: string
  status_code: number
  error_message: string | null
  error_count: number
}

export interface UsageModelWindowAggregate {
  providerCode: string
  model: string
  requestCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheReadCostUsd: number
  totalCostUsd: number
}

export interface UsageErrorWindowAggregate {
  errorGroup: string
  providerCode: string
  errorCode: string
  statusCode: number
  errorMessage?: string
  errorCount: number
}

export function aggregateUsageRowsForRange(rowsByDate: Map<string, UsageStatsDailyWindowRow[]>, range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): UsageWindowAggregate {
  const aggregate = emptyUsageWindowAggregate()
  for (const row of rowsForDateRange(rowsByDate, range)) {
    addUsageWindowAggregate(aggregate, row)
  }
  return aggregate
}

export function aggregateUsageTrendBuckets(rowsByDate: Map<string, UsageOverviewHourlyWindowRow[]>, range: AccountUsageStatsRange): Map<string, UsageWindowAggregate> {
  const buckets = new Map<string, UsageWindowAggregate>()
  const bucketHours = trendBucketHours(range)
  for (const row of rowsForDateRange(rowsByDate, range)) {
    const bucketKey = trendBucketKey(row.stat_hour, bucketHours)
    const bucket = buckets.get(bucketKey) ?? emptyUsageWindowAggregate()
    addUsageWindowAggregate(bucket, row)
    buckets.set(bucketKey, bucket)
  }
  return buckets
}

export function aggregateUsageModelRows(rowsByDate: Map<string, UsageModelWindowRow[]>, range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): UsageModelWindowAggregate[] {
  const buckets = new Map<string, UsageModelWindowAggregate>()
  for (const row of rowsForDateRange(rowsByDate, range)) {
    const providerCode = row.provider_code || 'unknown'
    const model = row.model || 'unknown'
    const key = `${providerCode}\n${model}`
    const bucket = buckets.get(key) ?? {
      providerCode,
      model,
      requestCount: 0,
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheReadCostUsd: 0,
      totalCostUsd: 0
    }
    bucket.requestCount += Number(row.request_count ?? 0)
    bucket.inputTokens += Number(row.input_tokens ?? 0)
    bucket.outputTokens += Number(row.output_tokens ?? 0)
    bucket.cacheReadTokens += Number(row.cache_read_tokens ?? 0)
    bucket.cacheReadCostUsd += Number(row.cache_read_cost_usd ?? 0)
    bucket.totalCostUsd += Number(row.total_cost_usd ?? 0)
    buckets.set(key, bucket)
  }
  return [...buckets.values()].sort((left, right) =>
    right.requestCount - left.requestCount
    || compareText(left.providerCode, right.providerCode)
    || compareText(left.model, right.model)
  )
}

export function aggregateUsageErrorRows(rowsByDate: Map<string, UsageErrorWindowRow[]>, range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): UsageErrorWindowAggregate[] {
  const buckets = new Map<string, UsageErrorWindowAggregate>()
  for (const row of rowsForDateRange(rowsByDate, range)) {
    const errorGroup = row.error_group || 'unknown'
    const providerCode = row.provider_code || 'unknown'
    const errorCode = row.error_code || 'unknown'
    const key = `${errorGroup}\n${providerCode}\n${errorCode}`
    const bucket = buckets.get(key) ?? {
      errorGroup,
      providerCode,
      errorCode,
      statusCode: 0,
      errorCount: 0
    }
    bucket.statusCode = Math.max(bucket.statusCode, Number(row.status_code ?? 0))
    bucket.errorMessage = maxText(bucket.errorMessage, row.error_message)
    bucket.errorCount += Number(row.error_count ?? 0)
    buckets.set(key, bucket)
  }
  return [...buckets.values()].sort((left, right) =>
    right.errorCount - left.errorCount
    || compareText(left.providerCode, right.providerCode)
    || compareText(left.errorCode, right.errorCode)
    || compareText(left.errorGroup, right.errorGroup)
  )
}

function emptyUsageWindowAggregate(): UsageWindowAggregate {
  return {
    requestCount: 0,
    successCount: 0,
    errorCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCostUsd: 0,
    totalCostUsd: 0,
    durationMsSum: 0,
    durationMsCount: 0,
    durationMsMax: 0,
    firstTokenMsSum: 0,
    firstTokenMsCount: 0,
    firstTokenMsMax: 0
  }
}

function addUsageWindowAggregate(target: UsageWindowAggregate, row: {
  request_count: number
  success_count?: number
  error_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_read_cost_usd: number
  total_cost_usd: number
  duration_ms_sum: number
  duration_ms_count: number
  duration_ms_max?: number
  first_token_ms_sum?: number
  first_token_ms_count?: number
  first_token_ms_max?: number
  last_used_at?: string | null
}): void {
  target.requestCount += Number(row.request_count ?? 0)
  target.successCount += Number(row.success_count ?? 0)
  target.errorCount += Number(row.error_count ?? 0)
  target.inputTokens += Number(row.input_tokens ?? 0)
  target.outputTokens += Number(row.output_tokens ?? 0)
  target.cacheReadTokens += Number(row.cache_read_tokens ?? 0)
  target.cacheReadCostUsd += Number(row.cache_read_cost_usd ?? 0)
  target.totalCostUsd += Number(row.total_cost_usd ?? 0)
  target.durationMsSum += Number(row.duration_ms_sum ?? 0)
  target.durationMsCount += Number(row.duration_ms_count ?? 0)
  target.durationMsMax = Math.max(target.durationMsMax, Number(row.duration_ms_max ?? 0))
  target.firstTokenMsSum += Number(row.first_token_ms_sum ?? 0)
  target.firstTokenMsCount += Number(row.first_token_ms_count ?? 0)
  target.firstTokenMsMax = Math.max(target.firstTokenMsMax, Number(row.first_token_ms_max ?? 0))
  target.lastUsedAt = latestText(target.lastUsedAt, row.last_used_at)
}

function latestText(left: string | undefined, right: string | null | undefined): string | undefined {
  if (!right) return left
  if (!left || right > left) return right
  return left
}

function maxText(left: string | undefined, right: string | null | undefined): string | undefined {
  if (right === null || right === undefined) return left
  if (left === undefined || right > left) return right
  return left
}
