import type { AccountUsageDailyPoint, AccountUsageStatsRange, AccountUsageSummary } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { getStatsDatabase } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import {
  dateKeysInRange,
  emptyAccountUsageDailyPoint,
  usageSummaryFromAggregate
} from './usage-stats-helpers.js'

export interface UsageStatsScopeRequest {
  rowKey: string
  systemAccountId: string
  scopeType: string
  scopeId: string
}

export interface UsageStatsDailySeries {
  rangeUsage: AccountUsageSummary
  dailyUsage: AccountUsageDailyPoint[]
}

type UsageStatsScopeAggregateRow = {
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
  system_account_id: string
  scope_type: string
  scope_id: string
  stat_date?: string
}

export function loadUsageDailySeriesForScopeRequests(scopes: UsageStatsScopeRequest[], range: AccountUsageStatsRange): Map<string, UsageStatsDailySeries> {
  const dateKeys = dateKeysInRange(range)
  const validScopes = scopes.filter((scope) => scope.rowKey && scope.systemAccountId && scope.scopeType && scope.scopeId)
  const result = new Map<string, UsageStatsDailySeries>()
  const rowKeysByScopeMapKey = new Map<string, Set<string>>()
  const scopeRowsByMapKey = new Map<string, UsageStatsScopeRequest>()
  for (const scope of validScopes) {
    result.set(scope.rowKey, emptyUsageDailySeries(dateKeys))
    const mapKey = usageStatsScopeMapKey(scope)
    scopeRowsByMapKey.set(mapKey, scope)
    const rowKeys = rowKeysByScopeMapKey.get(mapKey) ?? new Set<string>()
    rowKeys.add(scope.rowKey)
    rowKeysByScopeMapKey.set(mapKey, rowKeys)
  }
  const normalizedScopes = [...scopeRowsByMapKey.values()]
  if (!normalizedScopes.length || !dateKeys.length) return result

  const database = getStatsDatabase()
  const rows: UsageStatsScopeAggregateRow[] = []
  const scopesBySystemAccountId = new Map<string, UsageStatsScopeRequest[]>()
  for (const scope of normalizedScopes) {
    scopesBySystemAccountId.set(scope.systemAccountId, [...(scopesBySystemAccountId.get(scope.systemAccountId) ?? []), scope])
  }

  for (const [systemAccountId, systemScopes] of scopesBySystemAccountId) {
    const scopeTypes = [...new Set(systemScopes.map((scope) => scope.scopeType))]
    const scopeIds = [...new Set(systemScopes.map((scope) => scope.scopeId))]
    for (const scopeIdChunk of chunkValues(scopeIds, 400)) {
      rows.push(...database.prepare(`
        SELECT
          system_account_id,
          scope_type,
          scope_id,
          scope_id AS account_id,
          stat_date,
          request_count,
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
          total_cost_usd AS total_cost,
          last_used_at
        FROM usage_stats_daily
        WHERE system_account_id = ?
          AND scope_type IN (${sqlPlaceholders(scopeTypes.length)})
          AND scope_id IN (${sqlPlaceholders(scopeIdChunk.length)})
          AND stat_date >= ?
          AND stat_date <= ?
      `).all(systemAccountId, ...scopeTypes, ...scopeIdChunk, range.startDate, range.endDate) as unknown as UsageStatsScopeAggregateRow[])
      rows.push(...database.prepare(`
        SELECT
          system_account_id,
          scope_type,
          scope_id,
          scope_id AS account_id,
          NULL AS stat_date,
          request_count,
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
          total_cost_usd AS total_cost,
          last_used_at
        FROM usage_scope_range_windows
        WHERE system_account_id = ?
          AND scope_type IN (${sqlPlaceholders(scopeTypes.length)})
          AND scope_id IN (${sqlPlaceholders(scopeIdChunk.length)})
          AND start_date = ?
          AND end_date = ?
      `).all(systemAccountId, ...scopeTypes, ...scopeIdChunk, range.startDate, range.endDate) as unknown as UsageStatsScopeAggregateRow[])
    }
  }

  const dateIndex = new Map(dateKeys.map((statDate, index) => [statDate, index]))
  for (const row of rows) {
    const rowKeys = rowKeysByScopeMapKey.get(usageStatsRowMapKey(row))
    if (!rowKeys) continue
    const rowUsage = usageSummaryFromAggregate(row)
    for (const rowKey of rowKeys) {
      const series = result.get(rowKey)
      if (!series) continue
      if (!row.stat_date) {
        series.rangeUsage = rowUsage
        continue
      }
      const index = dateIndex.get(row.stat_date)
      if (index === undefined) continue
      series.dailyUsage[index] = { statDate: row.stat_date, ...rowUsage }
    }
  }

  return result
}

export async function loadUsageDailySeriesForScopeRequestsAsync(scopes: UsageStatsScopeRequest[], range: AccountUsageStatsRange): Promise<Map<string, UsageStatsDailySeries>> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return loadUsageDailySeriesForScopeRequests(scopes, range)
  }
  const dateKeys = dateKeysInRange(range)
  const validScopes = scopes.filter((scope) => scope.rowKey && scope.systemAccountId && scope.scopeType && scope.scopeId)
  const result = new Map<string, UsageStatsDailySeries>()
  const rowKeysByScopeMapKey = new Map<string, Set<string>>()
  const scopeRowsByMapKey = new Map<string, UsageStatsScopeRequest>()
  for (const scope of validScopes) {
    result.set(scope.rowKey, emptyUsageDailySeries(dateKeys))
    const mapKey = usageStatsScopeMapKey(scope)
    scopeRowsByMapKey.set(mapKey, scope)
    const rowKeys = rowKeysByScopeMapKey.get(mapKey) ?? new Set<string>()
    rowKeys.add(scope.rowKey)
    rowKeysByScopeMapKey.set(mapKey, rowKeys)
  }
  const normalizedScopes = [...scopeRowsByMapKey.values()]
  if (!normalizedScopes.length || !dateKeys.length) return result

  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows: UsageStatsScopeAggregateRow[] = []
  const scopesBySystemAccountId = new Map<string, UsageStatsScopeRequest[]>()
  for (const scope of normalizedScopes) {
    scopesBySystemAccountId.set(scope.systemAccountId, [...(scopesBySystemAccountId.get(scope.systemAccountId) ?? []), scope])
  }

  for (const [systemAccountId, systemScopes] of scopesBySystemAccountId) {
    const scopeTypes = [...new Set(systemScopes.map((scope) => scope.scopeType))]
    const scopeIds = [...new Set(systemScopes.map((scope) => scope.scopeId))]
    for (const scopeIdChunk of chunkValues(scopeIds, 400)) {
      rows.push(...await client.query<UsageStatsScopeAggregateRow>(`
        SELECT
          system_account_id,
          scope_type,
          scope_id,
          scope_id AS account_id,
          stat_date,
          request_count,
          input_tokens,
          output_tokens,
          cache_read_tokens,
          CAST(cache_read_cost_usd AS double precision) AS cache_read_cost_usd,
          cache_write_tokens,
          cache_write_1h_tokens,
          CAST(cache_write_cost_usd AS double precision) AS cache_write_cost_usd,
          thinking_tokens,
          input_image_tokens,
          output_image_tokens,
          CAST(total_cost_usd AS double precision) AS total_cost,
          last_used_at
        FROM ${usageWindowStatsTable(client, 'usage_stats_daily')}
        WHERE system_account_id = ?
          AND scope_type IN (${client.dialect.bindPlaceholders(scopeTypes.length)})
          AND scope_id IN (${client.dialect.bindPlaceholders(scopeIdChunk.length)})
          AND stat_date >= ?
          AND stat_date <= ?
      `, [systemAccountId, ...scopeTypes, ...scopeIdChunk, range.startDate, range.endDate]))
      rows.push(...await client.query<UsageStatsScopeAggregateRow>(`
        SELECT
          system_account_id,
          scope_type,
          scope_id,
          scope_id AS account_id,
          NULL AS stat_date,
          request_count,
          input_tokens,
          output_tokens,
          cache_read_tokens,
          CAST(cache_read_cost_usd AS double precision) AS cache_read_cost_usd,
          cache_write_tokens,
          cache_write_1h_tokens,
          CAST(cache_write_cost_usd AS double precision) AS cache_write_cost_usd,
          thinking_tokens,
          input_image_tokens,
          output_image_tokens,
          CAST(total_cost_usd AS double precision) AS total_cost,
          last_used_at
        FROM ${usageWindowStatsTable(client, 'usage_scope_range_windows')}
        WHERE system_account_id = ?
          AND scope_type IN (${client.dialect.bindPlaceholders(scopeTypes.length)})
          AND scope_id IN (${client.dialect.bindPlaceholders(scopeIdChunk.length)})
          AND start_date = ?
          AND end_date = ?
      `, [systemAccountId, ...scopeTypes, ...scopeIdChunk, range.startDate, range.endDate]))
    }
  }

  const dateIndex = new Map(dateKeys.map((statDate, index) => [statDate, index]))
  for (const row of rows) {
    const rowKeys = rowKeysByScopeMapKey.get(usageStatsRowMapKey(row))
    if (!rowKeys) continue
    const rowUsage = usageSummaryFromAggregate(row)
    for (const rowKey of rowKeys) {
      const series = result.get(rowKey)
      if (!series) continue
      if (!row.stat_date) {
        series.rangeUsage = rowUsage
        continue
      }
      const index = dateIndex.get(row.stat_date)
      if (index === undefined) continue
      series.dailyUsage[index] = { statDate: row.stat_date, ...rowUsage }
    }
  }

  return result
}

function emptyUsageDailySeries(dateKeys: string[]): UsageStatsDailySeries {
  return {
    rangeUsage: usageSummaryFromAggregate({
      request_count: 0,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_read_cost_usd: 0,
      cache_write_tokens: 0,
      cache_write_1h_tokens: 0,
      cache_write_cost_usd: 0,
      thinking_tokens: 0,
      input_image_tokens: 0,
      output_image_tokens: 0,
      total_cost: 0,
      last_used_at: null
    }),
    dailyUsage: dateKeys.map(emptyAccountUsageDailyPoint)
  }
}

function usageStatsScopeMapKey(scope: Pick<UsageStatsScopeRequest, 'systemAccountId' | 'scopeType' | 'scopeId'>): string {
  return `${scope.systemAccountId}\u0000${scope.scopeType}\u0000${scope.scopeId}`
}

function usageStatsRowMapKey(row: { system_account_id: string; scope_type: string; scope_id: string }): string {
  return `${row.system_account_id}\u0000${row.scope_type}\u0000${row.scope_id}`
}

function usageWindowStatsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_stats', tableName)
}
