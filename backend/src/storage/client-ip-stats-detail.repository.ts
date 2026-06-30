import type { AccountUsageStatsRange } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { getStatsDatabase } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { normalizeIpHash } from './client-ip-normalization.js'
import { clientIpUsageRangeWindowReady, clientIpUsageRangeWindowReadyAsync } from './client-ip-usage-range-windows.repository.js'
import type { ClientIpStatsSortField, ClientIpUsageSummary } from './client-ip-stats-list.repository.js'
import { pagedTotalUpperBound } from './query-utils.js'
import { loadAccountLookupMap, loadAccountLookupMapAsync } from './repository-lookups.js'
import { normalizeAccountUsageStatsRange, usageStatsTimezone, usageStatsTimezoneAsync } from './usage-stats-helpers.js'

export interface ClientIpAccountUsageRow {
  accountId: string
  accountName?: string
  rangeUsage: ClientIpUsageSummary
}

export interface ClientIpStatsDetailOptions {
  ipHash: string
  page?: number
  pageSize?: number
  startDate?: string
  endDate?: string
  sortField?: ClientIpStatsSortField
  sortOrder?: 'asc' | 'desc'
}

export interface ClientIpStatsDetailResult {
  ipHash: string
  aggregateIpKey: string
  lastSeenAt?: string
  items: ClientIpAccountUsageRow[]
  pageUpperBound: number
  hasMore: boolean
  page: number
  pageSize: number
  range: AccountUsageStatsRange
  rangeReady: boolean
}

const clientIpStatsDetailMaxWindowRows = 1001
const statsSchemaName = 'juhe_stats'

export function getClientIpStatsDetail(options: ClientIpStatsDetailOptions): ClientIpStatsDetailResult | undefined {
  const ipHash = normalizeIpHash(options.ipHash)
  if (!ipHash) return undefined
  const database = getStatsDatabase()
  const registry = database.prepare(`
    SELECT ip_hash, aggregate_ip_key, last_seen_at
    FROM client_ip_registry
    WHERE ip_hash = ?
    LIMIT 1
  `).get(ipHash) as ClientIpRegistryRow | undefined
  if (!registry) return undefined

  const timezone = usageStatsTimezone()
  const range = normalizeAccountUsageStatsRange(options, timezone)
  const rangeReady = clientIpUsageRangeWindowReady(database, range.startDate, range.endDate)
  const pageSize = boundedDetailPageSize(options.pageSize)
  const page = boundedDetailPage(options.page, pageSize)
  if (!rangeReady) {
    return {
      ipHash: registry.ip_hash,
      aggregateIpKey: registry.aggregate_ip_key,
      lastSeenAt: registry.last_seen_at ?? undefined,
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
  const orderBy = clientIpAccountStatsOrderBy(options.sortField, options.sortOrder)
  const rows = database.prepare(`
    SELECT
      account_id,
      request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens,
      cache_write_cost_usd, thinking_tokens, input_image_tokens,
      output_image_tokens, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      average_duration_ms,
      first_token_ms_sum, first_token_ms_count,
      average_first_token_ms,
      active_days, last_used_at, last_error_at
    FROM client_ip_account_usage_range_windows
    WHERE ip_hash = ?
      AND start_date = ?
      AND end_date = ?
    ORDER BY ${orderBy}
    LIMIT ? OFFSET ?
  `).all(ipHash, range.startDate, range.endDate, pageSize + 1, offset) as unknown as ClientIpAccountUsageRangeRow[]
  const pageRows = rows.slice(0, pageSize)
  const hasMore = rows.length > pageSize
  const accounts = loadAccountLookupMap(pageRows.map((row) => row.account_id))
  return {
    ipHash: registry.ip_hash,
    aggregateIpKey: registry.aggregate_ip_key,
    lastSeenAt: registry.last_seen_at ?? undefined,
    items: pageRows.map((row) => {
      const account = accounts.get(row.account_id)
      return {
        accountId: row.account_id,
        accountName: account?.name,
        rangeUsage: usageSummaryFromRow(row)
      }
    }),
    pageUpperBound: pagedTotalUpperBound(page, pageSize, pageRows.length, hasMore),
    hasMore,
    page,
    pageSize,
    range,
    rangeReady
  }
}

export async function getClientIpStatsDetailAsync(options: ClientIpStatsDetailOptions): Promise<ClientIpStatsDetailResult | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getClientIpStatsDetail(options)
  }
  const ipHash = normalizeIpHash(options.ipHash)
  if (!ipHash) return undefined
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const registry = await client.one<ClientIpRegistryRow>(`
    SELECT ip_hash, aggregate_ip_key, last_seen_at
    FROM ${statsTable(client, 'client_ip_registry')}
    WHERE ip_hash = ?
    LIMIT 1
  `, [ipHash])
  if (!registry) return undefined

  const timezone = await usageStatsTimezoneAsync()
  const range = normalizeAccountUsageStatsRange(options, timezone)
  const rangeReady = await clientIpUsageRangeWindowReadyAsync(client, range.startDate, range.endDate)
  const pageSize = boundedDetailPageSize(options.pageSize)
  const page = boundedDetailPage(options.page, pageSize)
  if (!rangeReady) {
    return {
      ipHash: registry.ip_hash,
      aggregateIpKey: registry.aggregate_ip_key,
      lastSeenAt: registry.last_seen_at ?? undefined,
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
  const orderBy = clientIpAccountStatsOrderBy(options.sortField, options.sortOrder)
  const rows = await client.query<ClientIpAccountUsageRangeRow>(`
    SELECT
      account_id,
      request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, cache_write_tokens, cache_write_1h_tokens,
      cache_write_cost_usd, thinking_tokens, input_image_tokens,
      output_image_tokens, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      average_duration_ms,
      first_token_ms_sum, first_token_ms_count,
      average_first_token_ms,
      active_days, last_used_at, last_error_at
    FROM ${statsTable(client, 'client_ip_account_usage_range_windows')}
    WHERE ip_hash = ?
      AND start_date = ?
      AND end_date = ?
    ORDER BY ${orderBy}
    LIMIT ? OFFSET ?
  `, [ipHash, range.startDate, range.endDate, pageSize + 1, offset])
  const pageRows = rows.slice(0, pageSize)
  const hasMore = rows.length > pageSize
  const accounts = await loadAccountLookupMapAsync(client, pageRows.map((row) => row.account_id))
  return {
    ipHash: registry.ip_hash,
    aggregateIpKey: registry.aggregate_ip_key,
    lastSeenAt: registry.last_seen_at ?? undefined,
    items: pageRows.map((row) => {
      const account = accounts.get(row.account_id)
      return {
        accountId: row.account_id,
        accountName: account?.name,
        rangeUsage: usageSummaryFromRow(row)
      }
    }),
    pageUpperBound: pagedTotalUpperBound(page, pageSize, pageRows.length, hasMore),
    hasMore,
    page,
    pageSize,
    range,
    rangeReady
  }
}

function clientIpAccountStatsOrderBy(field: ClientIpStatsSortField | undefined, order: 'asc' | 'desc' | undefined): string {
  const direction = order === 'asc' ? 'ASC' : 'DESC'
  const tieDirection = direction === 'ASC' ? 'DESC' : 'ASC'
  switch (field) {
    case 'successCount':
      return `success_count ${direction}, account_id ${tieDirection}`
    case 'errorCount':
      return `error_count ${direction}, account_id ${tieDirection}`
    case 'errorRate':
      return `CASE WHEN request_count > 0 THEN CAST(error_count AS REAL) / request_count ELSE 0 END ${direction}, account_id ${tieDirection}`
    case 'totalTokens':
      return `(input_tokens + output_tokens) ${direction}, account_id ${tieDirection}`
    case 'activeDays':
      return `active_days ${direction}, account_id ${tieDirection}`
    case 'lastUsedAt':
      return `last_used_at ${direction}, account_id ${tieDirection}`
    case 'totalCost':
      return `total_cost_usd ${direction}, account_id ${tieDirection}`
    case 'requestCount':
    default:
      return `request_count ${direction}, account_id ${tieDirection}`
  }
}

function usageSummaryFromRow(row: Partial<ClientIpAccountStatsUsageRow> | undefined): ClientIpUsageSummary {
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
    lastUsedAt: row?.last_used_at ?? undefined,
    lastErrorAt: row?.last_error_at ?? undefined
  }
}

function boundedDetailPage(value: unknown, pageSize: number): number {
  const number = Number(value)
  const maxPage = Math.max(1, Math.floor((clientIpStatsDetailMaxWindowRows - 1) / Math.max(1, Math.trunc(pageSize))))
  return Number.isFinite(number) ? Math.min(Math.max(1, Math.trunc(number)), maxPage) : 1
}

function boundedDetailPageSize(value: unknown): number {
  const number = Number(value)
  return Number.isFinite(number) ? Math.min(Math.max(1, Math.trunc(number)), 100) : 20
}

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable(statsSchemaName, tableName)
}

interface ClientIpRegistryRow {
  ip_hash: string
  aggregate_ip_key: string
  last_seen_at: string | null
}

interface ClientIpAccountStatsUsageRow {
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

interface ClientIpAccountUsageRangeRow extends ClientIpAccountStatsUsageRow {
  account_id: string
}
