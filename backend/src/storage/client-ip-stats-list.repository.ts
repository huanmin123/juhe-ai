import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import type { AccountUsageStatsRange } from '../domain/types.js'
import { getStatsDatabase, nowIso } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { pagedTotalUpperBound } from './query-utils.js'
import { normalizeAccountUsageStatsRange, startOfZonedDateKeyIso, usageStatsTimezone, usageStatsTimezoneAsync } from './usage-stats-helpers.js'
import { nextDateKey } from './usage-stats-window-helpers.js'
import { clientIpUsageRangeWindowReady, clientIpUsageRangeWindowReadyAsync } from './client-ip-usage-range-windows.repository.js'

export type ClientIpStatsSortField = 'requestCount' | 'successCount' | 'errorCount' | 'errorRate' | 'totalTokens' | 'totalCost' | 'activeDays' | 'lastUsedAt'
export type ClientIpPolicyFilter = 'all' | 'normal' | 'blacklisted'
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

export function listClientIpStats(options: ClientIpStatsListOptions = {}): ClientIpStatsListResult {
  const database = getStatsDatabase()
  const timezone = usageStatsTimezone()
  const range = normalizeAccountUsageStatsRange(options, timezone)
  const lastUsedRange = normalizeClientIpLastUsedRange(options, timezone)
  const rangeReady = clientIpUsageRangeWindowReady(database, range.startDate, range.endDate)
  const pageSize = boundedPageSize(options.pageSize)
  const page = boundedPage(options.page, pageSize)
  if (!rangeReady) {
    return {
      items: [],
      pageUpperBound: 0,
      hasMore: false,
      page,
      pageSize,
      range,
      rangeReady
    }
  }
  const offset = (page - 1) * pageSize
  const policyNow = nowIso()
  const where = buildClientIpRangeWhere(options, range, policyNow, lastUsedRange, timezone)
  const orderBy = clientIpStatsOrderBy(options.sortField, options.sortOrder, options.lastUsedSortScope)
  const fromClause = clientIpStatsFromClause(options)
  const rows = database.prepare(`
    SELECT
      registry.ip_hash, registry.aggregate_ip_key, registry.last_seen_at AS registry_last_seen_at,
      range_stats.request_count, range_stats.success_count, range_stats.error_count,
      range_stats.input_tokens, range_stats.output_tokens, range_stats.cache_read_tokens,
      range_stats.cache_read_cost_usd, range_stats.total_cost_usd,
      range_stats.duration_ms_sum, range_stats.duration_ms_count, range_stats.duration_ms_max,
      range_stats.average_duration_ms,
      range_stats.first_token_ms_sum, range_stats.first_token_ms_count,
      range_stats.average_first_token_ms,
      range_stats.active_days, range_stats.last_used_at, range_stats.last_error_at,
      CASE WHEN ${activePolicyExistsSql('registry.ip_hash')} THEN 1 ELSE 0 END AS blacklisted
    FROM ${fromClause}
    ${where.clause}
    ORDER BY ${orderBy}
    LIMIT ? OFFSET ?
  `).all(policyNow, ...where.params, pageSize + 1, offset) as unknown as ClientIpStatsRangeRow[]
  const pageRows = rows.slice(0, pageSize)
  const hasMore = rows.length > pageSize
  return {
    items: pageRows.map(mapClientIpStatsRangeRow),
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
  if (!rangeReady) {
    return {
      items: [],
      pageUpperBound: 0,
      hasMore: false,
      page,
      pageSize,
      range,
      rangeReady
    }
  }
  const offset = (page - 1) * pageSize
  const policyNow = nowIso()
  const where = buildClientIpRangeWhere(options, range, policyNow, lastUsedRange, timezone, statsTable(client, 'client_ip_policies'))
  const orderBy = clientIpStatsOrderBy(options.sortField, options.sortOrder, options.lastUsedSortScope)
  const fromClause = clientIpStatsFromClauseAsync(client, options)
  const rows = await client.query<ClientIpStatsRangeRow>(`
    SELECT
      registry.ip_hash, registry.aggregate_ip_key, registry.last_seen_at AS registry_last_seen_at,
      range_stats.request_count, range_stats.success_count, range_stats.error_count,
      range_stats.input_tokens, range_stats.output_tokens, range_stats.cache_read_tokens,
      range_stats.cache_read_cost_usd, range_stats.total_cost_usd,
      range_stats.duration_ms_sum, range_stats.duration_ms_count, range_stats.duration_ms_max,
      range_stats.average_duration_ms,
      range_stats.first_token_ms_sum, range_stats.first_token_ms_count,
      range_stats.average_first_token_ms,
      range_stats.active_days, range_stats.last_used_at, range_stats.last_error_at,
      CASE WHEN ${activePolicyExistsSql('registry.ip_hash', statsTable(client, 'client_ip_policies'))} THEN 1 ELSE 0 END AS blacklisted
    FROM ${fromClause}
    ${where.clause}
    ORDER BY ${orderBy}
    LIMIT ? OFFSET ?
  `, [policyNow, ...where.params, pageSize + 1, offset])
  const pageRows = rows.slice(0, pageSize)
  const hasMore = rows.length > pageSize
  return {
    items: pageRows.map(mapClientIpStatsRangeRow),
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
  range: AccountUsageStatsRange,
  policyNow: string,
  lastUsedRange: AccountUsageStatsRange | undefined,
  timezone: string,
  policyTableName = 'client_ip_policies'
): ClientIpRangeWhere {
  const clauses = ['range_stats.start_date = ?', 'range_stats.end_date = ?']
  const params: SQLInputValue[] = [range.startDate, range.endDate]
  const lastUsedWindow = lastUsedRange ? clientIpLastUsedIsoWindow(lastUsedRange, timezone) : undefined
  if (lastUsedWindow) {
    clauses.push('registry.last_seen_at >= ? AND registry.last_seen_at < ?')
    params.push(lastUsedWindow.startIso, lastUsedWindow.endExclusiveIso)
  }
  const keyword = options.keyword?.trim()
  if (keyword) {
    const keywordUpperBound = clientIpKeywordPrefixUpperBound(keyword)
    clauses.push('((registry.aggregate_ip_key >= ? AND registry.aggregate_ip_key < ?) OR (registry.client_ip >= ? AND registry.client_ip < ?))')
    params.push(keyword, keywordUpperBound, keyword, keywordUpperBound)
  }
  const status = options.status ?? 'all'
  if (status === 'blacklisted') {
    clauses.push(activePolicyExistsSql('registry.ip_hash', policyTableName))
    params.push(policyNow)
  } else if (status === 'normal') {
    clauses.push(`NOT ${activePolicyExistsSql('registry.ip_hash', policyTableName)}`)
    params.push(policyNow)
  }
  return {
    clause: `WHERE ${clauses.join(' AND ')}`,
    params
  }
}

function normalizeClientIpLastUsedRange(options: ClientIpStatsListOptions, timezone: string): AccountUsageStatsRange | undefined {
  if (!options.lastUsedStartDate && !options.lastUsedEndDate) return undefined
  return normalizeAccountUsageStatsRange({
    startDate: options.lastUsedStartDate,
    endDate: options.lastUsedEndDate
  }, timezone)
}

function clientIpLastUsedIsoWindow(range: AccountUsageStatsRange, timezone: string): { startIso: string; endExclusiveIso: string } | undefined {
  const startIso = startOfZonedDateKeyIso(range.startDate, timezone)
  const endExclusiveIso = startOfZonedDateKeyIso(nextDateKey(range.endDate), timezone)
  if (!startIso || !endExclusiveIso) return undefined
  return { startIso, endExclusiveIso }
}

function activePolicyExistsSql(ipHashExpression: string, policyTableName = 'client_ip_policies'): string {
  return `EXISTS (
    SELECT 1
    FROM ${policyTableName} active_policies
    WHERE active_policies.status = 'active'
      AND active_policies.ip_hash = ${ipHashExpression}
      AND (active_policies.expires_at IS NULL OR active_policies.expires_at > ?)
    LIMIT 1
  )`
}

function clientIpStatsFromClause(options: ClientIpStatsListOptions): string {
  if (options.sortField === 'lastUsedAt' && options.lastUsedSortScope === 'global') {
    return 'client_ip_registry registry INDEXED BY idx_client_ip_registry_last_seen INNER JOIN client_ip_usage_range_windows range_stats ON registry.ip_hash = range_stats.ip_hash'
  }
  if (hasClientIpKeyword(options)) {
    return 'client_ip_registry registry INNER JOIN client_ip_usage_range_windows range_stats ON registry.ip_hash = range_stats.ip_hash'
  }
  return 'client_ip_usage_range_windows range_stats INNER JOIN client_ip_registry registry ON registry.ip_hash = range_stats.ip_hash'
}

function clientIpStatsFromClauseAsync(client: DatabaseClient, options: ClientIpStatsListOptions): string {
  const registryTable = statsTable(client, 'client_ip_registry')
  const rangeTable = statsTable(client, 'client_ip_usage_range_windows')
  if (options.sortField === 'lastUsedAt' && options.lastUsedSortScope === 'global') {
    return `${registryTable} registry INNER JOIN ${rangeTable} range_stats ON registry.ip_hash = range_stats.ip_hash`
  }
  if (hasClientIpKeyword(options)) {
    return `${registryTable} registry INNER JOIN ${rangeTable} range_stats ON registry.ip_hash = range_stats.ip_hash`
  }
  return `${rangeTable} range_stats INNER JOIN ${registryTable} registry ON registry.ip_hash = range_stats.ip_hash`
}

function hasClientIpKeyword(options: ClientIpStatsListOptions): boolean {
  return Boolean(options.keyword?.trim())
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
      return `range_stats.success_count ${direction}, range_stats.ip_hash ASC`
    case 'errorCount':
      return `range_stats.error_count ${direction}, range_stats.ip_hash ASC`
    case 'errorRate':
      return `CASE WHEN range_stats.request_count > 0 THEN CAST(range_stats.error_count AS REAL) / range_stats.request_count ELSE 0 END ${direction}, range_stats.ip_hash ASC`
    case 'totalTokens':
      return `(range_stats.input_tokens + range_stats.output_tokens) ${direction}, range_stats.ip_hash ASC`
    case 'activeDays':
      return `range_stats.active_days ${direction}, range_stats.ip_hash ASC`
    case 'lastUsedAt':
      return lastUsedSortScope === 'global'
        ? `registry.last_seen_at ${direction}, registry.ip_hash ${direction === 'ASC' ? 'DESC' : 'ASC'}`
        : `range_stats.last_used_at ${direction}, range_stats.ip_hash ASC`
    case 'requestCount':
      return `range_stats.request_count ${direction}, range_stats.ip_hash ASC`
    case 'totalCost':
      return `range_stats.total_cost_usd ${direction}, range_stats.ip_hash ASC`
    default:
      return 'range_stats.request_count DESC, range_stats.ip_hash ASC'
  }
}

function mapClientIpStatsRangeRow(row: ClientIpStatsRangeRow): ClientIpStatsRow {
  const rangeUsage = usageSummaryFromRow(row)
  const blacklisted = Number(row.blacklisted ?? 0) > 0
  return {
    ipHash: row.ip_hash,
    aggregateIpKey: row.aggregate_ip_key,
    lastSeenAt: row.registry_last_seen_at ?? undefined,
    status: blacklisted ? 'blacklisted' : 'normal',
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
    totalTokens: inputTokens + outputTokens,
    totalCost: Number(row?.total_cost_usd ?? 0),
    activeDays: Number(row?.active_days ?? 0),
    averageDurationMs: Number.isFinite(averageDurationMs) ? averageDurationMs : undefined,
    averageFirstTokenMs: Number.isFinite(averageFirstTokenMs) ? averageFirstTokenMs : undefined,
    maxDurationMs: durationMsCount > 0 && durationMsMax > 0 ? durationMsMax : undefined,
    lastUsedAt: row?.last_used_at ?? undefined,
    lastErrorAt: row?.last_error_at ?? undefined
  }
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
  blacklisted: number
}
