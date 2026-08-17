import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import type { AccountUsageStatsRange } from '../domain/types.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../shared/rfc3339.js'
import { getStatsDatabase, nowIso } from './database.js'
import { listActiveClientIpPolicies, listActiveClientIpPoliciesAsync, type ActiveClientIpPolicy } from './client-ip-policy.repository.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { pagedTotalUpperBound } from './query-utils.js'
import { normalizeAccountUsageStatsRange, startOfZonedDateKeyIso, usageStatsTimezone, usageStatsTimezoneAsync } from './usage-stats-helpers.js'
import { nextDateKey } from './usage-stats-window-helpers.js'
import { clientIpUsageRangeWindowReady, clientIpUsageRangeWindowReadyAsync } from './client-ip-usage-range-windows.repository.js'

export type ClientIpStatsSortField = 'requestCount' | 'successCount' | 'errorCount' | 'errorRate' | 'totalTokens' | 'totalCost' | 'activeDays' | 'lastUsedAt'
export type ClientIpPolicyFilter = 'all' | 'normal' | 'blacklisted' | 'allowlisted'
export type ClientIpLastUsedSortScope = 'range' | 'global'

export interface ClientIpUsageSummary {
  requestCount: number
  successCount: number
  errorCount: number
  errorRate: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheReadCost: number
  cacheWriteTokens: number
  cacheWrite1hTokens: number
  cacheWriteCost: number
  thinkingTokens: number
  inputImageTokens: number
  outputImageTokens: number
  totalTokens: number
  totalCost: number
  activeDays: number
  averageDurationMs?: number
  averageFirstTokenMs?: number
  maxDurationMs?: number
  lastUsedAt?: string
  lastErrorAt?: string
}

export interface ClientIpStatsRow {
  ipHash: string
  aggregateIpKey: string
  lastSeenAt?: string
  status: ClientIpPolicyFilter
  rangeUsage: ClientIpUsageSummary
}

export interface ClientIpStatsListOptions {
  page?: number
  pageSize?: number
  keyword?: string
  status?: ClientIpPolicyFilter
  startDate?: string
  endDate?: string
  lastUsedStartDate?: string
  lastUsedEndDate?: string
  lastUsedSortScope?: ClientIpLastUsedSortScope
  sortField?: ClientIpStatsSortField
  sortOrder?: 'asc' | 'desc'
}

export interface ClientIpStatsListResult {
  items: ClientIpStatsRow[]
  pageUpperBound: number
  hasMore: boolean
  page: number
  pageSize: number
  range: AccountUsageStatsRange
  rangeReady: boolean
}

const clientIpStatsMaxListWindowRows = 1001
const statsSchemaName = 'juhe_stats'

interface ClientIpRangeWhere {
  clause: string
  params: SQLInputValue[]
}

interface ClientIpPolicySets {
  blacklist: Set<string>
  allowlist: Set<string>
}

interface ClientIpLastUsedEpochWindow {
  startMs: number
  endExclusiveMs: number
}

export function listClientIpStats(options: ClientIpStatsListOptions = {}): ClientIpStatsListResult {
  const database = getStatsDatabase()
  const timezone = usageStatsTimezone()
  const range = normalizeAccountUsageStatsRange(options, timezone)
  const lastUsedRange = normalizeClientIpLastUsedRange(options, timezone)
  const rangeReady = clientIpUsageRangeWindowReady(database, range.startDate, range.endDate)
  const pageSize = boundedPageSize(options.pageSize)
  const page = boundedPage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const policySets = activeClientIpPolicySets(listActiveClientIpPolicies())
  const policyNow = nowIso()
  const queryStartDate = rangeReady ? range.startDate : ''
  const queryEndDate = rangeReady ? range.endDate : ''
  const where = buildClientIpRangeWhere(options, policyNow, 'client_ip_policies', 'sqlite')
  const orderBy = clientIpStatsOrderBy(options.sortField, options.sortOrder, options.lastUsedSortScope)
  const fromClause = clientIpStatsFromClause(options)
  const rows = database.prepare(`
    SELECT
      registry.ip_hash, registry.aggregate_ip_key, registry.last_seen_at AS registry_last_seen_at,
      range_stats.request_count, range_stats.success_count, range_stats.error_count,
      range_stats.input_tokens, range_stats.output_tokens, range_stats.cache_read_tokens,
      range_stats.cache_read_cost_usd, range_stats.cache_write_tokens, range_stats.cache_write_1h_tokens,
      range_stats.cache_write_cost_usd, range_stats.thinking_tokens, range_stats.input_image_tokens,
      range_stats.output_image_tokens, range_stats.total_cost_usd,
      range_stats.duration_ms_sum, range_stats.duration_ms_count, range_stats.duration_ms_max,
      range_stats.average_duration_ms,
      range_stats.first_token_ms_sum, range_stats.first_token_ms_count,
      range_stats.average_first_token_ms,
      range_stats.active_days, range_stats.last_used_at, range_stats.last_error_at
    FROM ${fromClause}
    ${where.clause}
    ORDER BY ${orderBy}
    LIMIT ?
  `).all(queryStartDate, queryEndDate, ...where.params, clientIpStatsMaxListWindowRows) as unknown as ClientIpStatsRangeRow[]
  const lastUsedWindow = lastUsedRange ? clientIpLastUsedEpochWindow(lastUsedRange, timezone) : undefined
  const filteredRows = rows
    .map((row) => mapClientIpStatsRangeRow(row, policySets))
    .filter((row) => clientIpStatsRowMatchesLastUsedWindow(row, lastUsedWindow))
  const sortedRows = sortClientIpStatsRows(filteredRows, options.sortField, options.sortOrder, options.lastUsedSortScope)
  const pageRows = sortedRows.slice(offset, offset + pageSize)
  const hasMore = sortedRows.length > offset + pageSize || rows.length === clientIpStatsMaxListWindowRows
  return {
    items: pageRows,
    pageUpperBound: pagedTotalUpperBound(page, pageSize, pageRows.length, hasMore),
    hasMore,
    page,
    pageSize,
    range,
    rangeReady
  }
}

export async function listClientIpStatsAsync(options: ClientIpStatsListOptions = {}): Promise<ClientIpStatsListResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listClientIpStats(options)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const timezone = await usageStatsTimezoneAsync()
  const range = normalizeAccountUsageStatsRange(options, timezone)
  const lastUsedRange = normalizeClientIpLastUsedRange(options, timezone)
  const rangeReady = await clientIpUsageRangeWindowReadyAsync(client, range.startDate, range.endDate)
  const pageSize = boundedPageSize(options.pageSize)
  const page = boundedPage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const policySets = activeClientIpPolicySets(await listActiveClientIpPoliciesAsync())
  const policyNow = nowIso()
  const queryStartDate = rangeReady ? range.startDate : ''
  const queryEndDate = rangeReady ? range.endDate : ''
  const where = buildClientIpRangeWhere(options, policyNow, statsTable(client, 'client_ip_policies'), 'postgres')
  const orderBy = clientIpStatsOrderBy(options.sortField, options.sortOrder, options.lastUsedSortScope)
  const fromClause = clientIpStatsFromClauseAsync(client, options)
  const rows = await client.query<ClientIpStatsRangeRow>(`
    SELECT
      registry.ip_hash, registry.aggregate_ip_key, registry.last_seen_at AS registry_last_seen_at,
      range_stats.request_count, range_stats.success_count, range_stats.error_count,
      range_stats.input_tokens, range_stats.output_tokens, range_stats.cache_read_tokens,
      range_stats.cache_read_cost_usd, range_stats.cache_write_tokens, range_stats.cache_write_1h_tokens,
      range_stats.cache_write_cost_usd, range_stats.thinking_tokens, range_stats.input_image_tokens,
      range_stats.output_image_tokens, range_stats.total_cost_usd,
      range_stats.duration_ms_sum, range_stats.duration_ms_count, range_stats.duration_ms_max,
      range_stats.average_duration_ms,
      range_stats.first_token_ms_sum, range_stats.first_token_ms_count,
      range_stats.average_first_token_ms,
      range_stats.active_days, range_stats.last_used_at, range_stats.last_error_at
    FROM ${fromClause}
    ${where.clause}
    ORDER BY ${orderBy}
    LIMIT ?
  `, [queryStartDate, queryEndDate, ...where.params, clientIpStatsMaxListWindowRows])
  const lastUsedWindow = lastUsedRange ? clientIpLastUsedEpochWindow(lastUsedRange, timezone) : undefined
  const filteredRows = rows
    .map((row) => mapClientIpStatsRangeRow(row, policySets))
    .filter((row) => clientIpStatsRowMatchesLastUsedWindow(row, lastUsedWindow))
  const sortedRows = sortClientIpStatsRows(filteredRows, options.sortField, options.sortOrder, options.lastUsedSortScope)
  const pageRows = sortedRows.slice(offset, offset + pageSize)
  const hasMore = sortedRows.length > offset + pageSize || rows.length === clientIpStatsMaxListWindowRows
  return {
    items: pageRows,
    pageUpperBound: pagedTotalUpperBound(page, pageSize, pageRows.length, hasMore),
    hasMore,
    page,
    pageSize,
    range,
    rangeReady
  }
}

function buildClientIpRangeWhere(
  options: ClientIpStatsListOptions,
  policyNow: string,
  policyTableName: string,
  dialect: 'sqlite' | 'postgres'
): ClientIpRangeWhere {
  const clauses: string[] = []
  const params: SQLInputValue[] = []
  const keyword = options.keyword?.trim()
  if (keyword) {
    const keywordUpperBound = clientIpKeywordPrefixUpperBound(keyword)
    clauses.push('((registry.aggregate_ip_key >= ? AND registry.aggregate_ip_key < ?) OR (registry.client_ip >= ? AND registry.client_ip < ?))')
    params.push(keyword, keywordUpperBound, keyword, keywordUpperBound)
  }
  const status = options.status ?? 'all'
  if (status === 'blacklisted') {
    clauses.push(activePolicyExistsSql('registry.ip_hash', 'blacklist', policyTableName, dialect))
    params.push(policyNow)
  } else if (status === 'allowlisted') {
    clauses.push(activePolicyExistsSql('registry.ip_hash', 'allowlist', policyTableName, dialect))
    params.push(policyNow)
  } else if (status === 'normal') {
    clauses.push(`NOT ${activePolicyExistsSql('registry.ip_hash', 'blacklist', policyTableName, dialect)}`)
    clauses.push(`NOT ${activePolicyExistsSql('registry.ip_hash', 'allowlist', policyTableName, dialect)}`)
    params.push(policyNow, policyNow)
  }
  return {
    clause: clauses.length > 0 ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function activePolicyExistsSql(
  ipHashExpression: string,
  policyType: 'blacklist' | 'allowlist',
  policyTableName: string,
  dialect: 'sqlite' | 'postgres'
): string {
  const expiresAtAfterNow = dialect === 'postgres'
    ? "EXTRACT(EPOCH FROM active_policies.expires_at::timestamptz) > EXTRACT(EPOCH FROM ?::timestamptz)"
    : 'unixepoch(active_policies.expires_at) > unixepoch(?)'
  return `EXISTS (
    SELECT 1
    FROM ${policyTableName} active_policies
    WHERE active_policies.status = 'active'
      AND active_policies.policy_type = '${policyType}'
      AND active_policies.ip_hash = ${ipHashExpression}
      AND (active_policies.expires_at IS NULL OR ${expiresAtAfterNow})
    LIMIT 1
  )`
}

function normalizeClientIpLastUsedRange(options: ClientIpStatsListOptions, timezone: string): AccountUsageStatsRange | undefined {
  if (!options.lastUsedStartDate && !options.lastUsedEndDate) return undefined
  return normalizeAccountUsageStatsRange({
    startDate: options.lastUsedStartDate,
    endDate: options.lastUsedEndDate
  }, timezone)
}

function clientIpLastUsedEpochWindow(range: AccountUsageStatsRange, timezone: string): ClientIpLastUsedEpochWindow | undefined {
  const startIso = startOfZonedDateKeyIso(range.startDate, timezone)
  const endExclusiveIso = startOfZonedDateKeyIso(nextDateKey(range.endDate), timezone)
  if (!startIso || !endExclusiveIso) return undefined
  const startMs = rfc3339InstantMilliseconds(startIso)
  const endExclusiveMs = rfc3339InstantMilliseconds(endExclusiveIso)
  if (startMs === undefined || endExclusiveMs === undefined) {
    throw new Error('客户端 IP 最后使用时间窗口必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  return { startMs, endExclusiveMs }
}

function clientIpStatsFromClause(options: ClientIpStatsListOptions): string {
  const rangeJoin = `LEFT JOIN client_ip_usage_range_windows range_stats
      ON range_stats.ip_hash = registry.ip_hash
      AND range_stats.start_date = ?
      AND range_stats.end_date = ?`
  void options
  return `client_ip_registry registry ${rangeJoin}`
}

function clientIpStatsFromClauseAsync(client: DatabaseClient, _options: ClientIpStatsListOptions): string {
  const registryTable = statsTable(client, 'client_ip_registry')
  const rangeTable = statsTable(client, 'client_ip_usage_range_windows')
  return `${registryTable} registry LEFT JOIN ${rangeTable} range_stats
    ON range_stats.ip_hash = registry.ip_hash
    AND range_stats.start_date = ?
    AND range_stats.end_date = ?`
}

function clientIpKeywordPrefixUpperBound(value: string): string {
  for (let index = value.length - 1; index >= 0; index -= 1) {
    const code = value.charCodeAt(index)
    if (code < 0xffff) {
      return `${value.slice(0, index)}${String.fromCharCode(code + 1)}`
    }
  }
  return `${value}\uffff`
}

function clientIpStatsOrderBy(field: ClientIpStatsSortField | undefined, order: 'asc' | 'desc' | undefined, lastUsedSortScope: ClientIpLastUsedSortScope = 'range'): string {
  const direction = order === 'asc' ? 'ASC' : 'DESC'
  switch (field) {
    case 'successCount':
      return `COALESCE(range_stats.success_count, 0) ${direction}, registry.ip_hash ASC`
    case 'errorCount':
      return `COALESCE(range_stats.error_count, 0) ${direction}, registry.ip_hash ASC`
    case 'errorRate':
      return `CASE WHEN COALESCE(range_stats.request_count, 0) > 0 THEN CAST(range_stats.error_count AS REAL) / range_stats.request_count ELSE 0 END ${direction}, registry.ip_hash ASC`
    case 'totalTokens':
      return `(COALESCE(range_stats.input_tokens, 0) + COALESCE(range_stats.output_tokens, 0)) ${direction}, registry.ip_hash ASC`
    case 'activeDays':
      return `COALESCE(range_stats.active_days, 0) ${direction}, registry.ip_hash ASC`
    case 'lastUsedAt':
      void lastUsedSortScope
      return 'registry.ip_hash ASC'
    case 'requestCount':
      return `COALESCE(range_stats.request_count, 0) ${direction}, registry.ip_hash ASC`
    case 'totalCost':
      return `COALESCE(range_stats.total_cost_usd, 0) ${direction}, registry.ip_hash ASC`
    default:
      return 'COALESCE(range_stats.request_count, 0) DESC, registry.ip_hash ASC'
  }
}

function mapClientIpStatsRangeRow(row: ClientIpStatsRangeRow, policySets: ClientIpPolicySets): ClientIpStatsRow {
  const rangeUsage = usageSummaryFromRow(row)
  const blacklisted = policySets.blacklist.has(row.ip_hash)
  const allowlisted = policySets.allowlist.has(row.ip_hash)
  return {
    ipHash: row.ip_hash,
    aggregateIpKey: row.aggregate_ip_key,
    lastSeenAt: optionalTimestamp(row.registry_last_seen_at, 'client_ip_registry.last_seen_at'),
    status: blacklisted ? 'blacklisted' : allowlisted ? 'allowlisted' : 'normal',
    rangeUsage
  }
}

function usageSummaryFromRow(row: Partial<ClientIpStatsUsageRow> | undefined): ClientIpUsageSummary {
  const requestCount = Number(row?.request_count ?? 0)
  const successCount = Number(row?.success_count ?? 0)
  const errorCount = Number(row?.error_count ?? 0)
  const inputTokens = Number(row?.input_tokens ?? 0)
  const outputTokens = Number(row?.output_tokens ?? 0)
  const durationMsCount = Number(row?.duration_ms_count ?? 0)
  const firstTokenMsCount = Number(row?.first_token_ms_count ?? 0)
  const durationMsMax = Number(row?.duration_ms_max ?? 0)
  const averageDurationMs = row?.average_duration_ms == null
    ? (durationMsCount > 0 ? Number(row?.duration_ms_sum ?? 0) / durationMsCount : undefined)
    : Number(row.average_duration_ms)
  const averageFirstTokenMs = row?.average_first_token_ms == null
    ? (firstTokenMsCount > 0 ? Number(row?.first_token_ms_sum ?? 0) / firstTokenMsCount : undefined)
    : Number(row.average_first_token_ms)
  return {
    requestCount,
    successCount,
    errorCount,
    errorRate: requestCount > 0 ? errorCount / requestCount : 0,
    inputTokens,
    outputTokens,
    cacheReadTokens: Number(row?.cache_read_tokens ?? 0),
    cacheReadCost: Number(row?.cache_read_cost_usd ?? 0),
    cacheWriteTokens: Number(row?.cache_write_tokens ?? 0),
    cacheWrite1hTokens: Number(row?.cache_write_1h_tokens ?? 0),
    cacheWriteCost: Number(row?.cache_write_cost_usd ?? 0),
    thinkingTokens: Number(row?.thinking_tokens ?? 0),
    inputImageTokens: Number(row?.input_image_tokens ?? 0),
    outputImageTokens: Number(row?.output_image_tokens ?? 0),
    totalTokens: inputTokens + outputTokens,
    totalCost: Number(row?.total_cost_usd ?? 0),
    activeDays: Number(row?.active_days ?? 0),
    averageDurationMs: Number.isFinite(averageDurationMs) ? averageDurationMs : undefined,
    averageFirstTokenMs: Number.isFinite(averageFirstTokenMs) ? averageFirstTokenMs : undefined,
    maxDurationMs: durationMsCount > 0 && durationMsMax > 0 ? durationMsMax : undefined,
    lastUsedAt: optionalTimestamp(row?.last_used_at, 'client_ip_usage_range_windows.last_used_at'),
    lastErrorAt: optionalTimestamp(row?.last_error_at, 'client_ip_usage_range_windows.last_error_at')
  }
}

function activeClientIpPolicySets(policies: ActiveClientIpPolicy[]): ClientIpPolicySets {
  const sets: ClientIpPolicySets = {
    blacklist: new Set<string>(),
    allowlist: new Set<string>()
  }
  for (const policy of policies) {
    sets[policy.policyType].add(policy.ipHash)
  }
  return sets
}

function clientIpStatsRowMatchesLastUsedWindow(row: ClientIpStatsRow, window: ClientIpLastUsedEpochWindow | undefined): boolean {
  if (!window) return true
  if (row.lastSeenAt === undefined) return false
  const lastSeenAtMs = rfc3339InstantMilliseconds(row.lastSeenAt)
  if (lastSeenAtMs === undefined) throw new Error('client_ip_registry.last_seen_at必须是带 Z 或数值 offset 的 RFC3339 时间')
  return lastSeenAtMs >= window.startMs && lastSeenAtMs < window.endExclusiveMs
}

function sortClientIpStatsRows(
  rows: ClientIpStatsRow[],
  field: ClientIpStatsSortField | undefined,
  order: 'asc' | 'desc' | undefined,
  lastUsedSortScope: ClientIpLastUsedSortScope = 'range'
): ClientIpStatsRow[] {
  if (field !== 'lastUsedAt') return rows
  const direction = order === 'asc' ? 1 : -1
  return [...rows].sort((left, right) => {
    const leftTime = optionalTimestampMilliseconds(
      lastUsedSortScope === 'global' ? left.lastSeenAt : left.rangeUsage.lastUsedAt,
      '客户端 IP lastUsedAt'
    )
    const rightTime = optionalTimestampMilliseconds(
      lastUsedSortScope === 'global' ? right.lastSeenAt : right.rangeUsage.lastUsedAt,
      '客户端 IP lastUsedAt'
    )
    if (leftTime !== rightTime) {
      if (leftTime === undefined) return -direction
      if (rightTime === undefined) return direction
      return (leftTime - rightTime) * direction
    }
    const tieDirection = lastUsedSortScope === 'global' && direction === 1 ? -1 : 1
    return left.ipHash.localeCompare(right.ipHash) * tieDirection
  })
}

function optionalTimestamp(value: unknown, label: string): string | undefined {
  if (value === undefined || value === null) return undefined
  return requiredRfc3339Instant(value, label)
}

function optionalTimestampMilliseconds(value: string | undefined, label: string): number | undefined {
  if (value === undefined) return undefined
  const timestamp = rfc3339InstantMilliseconds(value)
  if (timestamp === undefined) throw new Error(`${label}必须是带 Z 或数值 offset 的 RFC3339 时间`)
  return timestamp
}

function boundedPage(value: unknown, pageSize: number): number {
  const number = Number(value)
  const maxPage = Math.max(1, Math.floor((clientIpStatsMaxListWindowRows - 1) / Math.max(1, Math.trunc(pageSize))))
  return Number.isFinite(number) ? Math.min(Math.max(1, Math.trunc(number)), maxPage) : 1
}

function boundedPageSize(value: unknown): number {
  const number = Number(value)
  return Number.isFinite(number) ? Math.min(Math.max(1, Math.trunc(number)), 100) : 20
}

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable(statsSchemaName, tableName)
}

interface ClientIpStatsUsageRow {
  request_count: number
  success_count: number
  error_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_read_cost_usd: number
  cache_write_tokens: number
  cache_write_1h_tokens: number
  cache_write_cost_usd: number
  thinking_tokens: number
  input_image_tokens: number
  output_image_tokens: number
  total_cost_usd: number
  duration_ms_sum: number
  duration_ms_count: number
  duration_ms_max: number
  average_duration_ms: number | null
  first_token_ms_sum: number
  first_token_ms_count: number
  average_first_token_ms: number | null
  active_days: number
  last_used_at: string | null
  last_error_at: string | null
}

interface ClientIpStatsRangeRow extends ClientIpStatsUsageRow {
  ip_hash: string
  aggregate_ip_key: string
  registry_last_seen_at: string | null
}
