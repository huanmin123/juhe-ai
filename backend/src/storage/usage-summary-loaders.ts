import type { AccountUsageStatsRange, AccountUsageSummary } from '../domain/types.js'
import { getDatabase } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { addUsageSummaries, emptyAccountUsageSummary, usageStatsBucketPlan, usageSummaryFromAggregate } from './usage-stats-helpers.js'

type AuthorizationUsageScopeType = 'account_authorization' | 'group_authorization'
type UsageSummaryScopeType = 'account' | 'group' | 'api_key' | AuthorizationUsageScopeType

export interface UsageSummaryScopeRequest {
  rowKey: string
  systemAccountId: string
  scopeId: string
}

interface UsageSummaryAggregateRow {
  system_account_id: string
  scope_id: string
  request_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  total_cost: number
  last_used_at: string | null
}

interface UsageRangeBucketSource {
  tableName: 'usage_stats_monthly' | 'usage_stats_weekly' | 'usage_stats_daily'
  timeColumn: 'stat_month' | 'stat_week' | 'stat_date'
  timeValues: string[]
}

function usageStatsSource(statDate?: string): { tableName: 'usage_stats_daily' | 'usage_stats_totals'; dateClause: string } {
  return statDate
    ? { tableName: 'usage_stats_daily', dateClause: ' AND stat_date = ?' }
    : { tableName: 'usage_stats_totals', dateClause: '' }
}

function usageSummaryScopeMapKey(scope: Pick<UsageSummaryScopeRequest, 'systemAccountId' | 'scopeId'>): string {
  return `${scope.systemAccountId}\u0000${scope.scopeId}`
}

function usageSummaryRowMapKey(row: Pick<UsageSummaryAggregateRow, 'system_account_id' | 'scope_id'>): string {
  return `${row.system_account_id}\u0000${row.scope_id}`
}

function loadUsageSummariesForScopeRequests(scopes: UsageSummaryScopeRequest[], scopeType: UsageSummaryScopeType, statDate?: string): Map<string, AccountUsageSummary> {
  const validScopes = scopes.filter((scope) => scope.rowKey && scope.systemAccountId && scope.scopeId)
  const result = new Map<string, AccountUsageSummary>()
  const rowKeysByScopeMapKey = new Map<string, Set<string>>()
  const scopeRowsByMapKey = new Map<string, UsageSummaryScopeRequest>()
  for (const scope of validScopes) {
    const mapKey = usageSummaryScopeMapKey(scope)
    scopeRowsByMapKey.set(mapKey, scope)
    const rowKeys = rowKeysByScopeMapKey.get(mapKey) ?? new Set<string>()
    rowKeys.add(scope.rowKey)
    rowKeysByScopeMapKey.set(mapKey, rowKeys)
  }
  const normalizedScopes = [...scopeRowsByMapKey.values()]
  if (!normalizedScopes.length) return result

  const source = usageStatsSource(statDate)
  const database = getDatabase()
  const rows: UsageSummaryAggregateRow[] = []
  const scopesBySystemAccountId = new Map<string, UsageSummaryScopeRequest[]>()
  for (const scope of normalizedScopes) {
    scopesBySystemAccountId.set(scope.systemAccountId, [...(scopesBySystemAccountId.get(scope.systemAccountId) ?? []), scope])
  }

  for (const [systemAccountId, systemScopes] of scopesBySystemAccountId) {
    const scopeIds = [...new Set(systemScopes.map((scope) => scope.scopeId))]
    for (const scopeIdChunk of chunkValues(scopeIds, 400)) {
      rows.push(...database.prepare(`
        SELECT
          system_account_id,
          scope_id,
          request_count,
          input_tokens,
          output_tokens,
          cache_read_tokens,
          total_cost_usd AS total_cost,
          last_used_at
        FROM ${source.tableName}
        WHERE system_account_id = ?
          AND scope_type = ?${source.dateClause}
          AND scope_id IN (${sqlPlaceholders(scopeIdChunk.length)})
      `).all(...(statDate
        ? [systemAccountId, scopeType, statDate, ...scopeIdChunk]
        : [systemAccountId, scopeType, ...scopeIdChunk]
      )) as unknown as UsageSummaryAggregateRow[])
    }
  }

  for (const row of rows) {
    const rowKeys = rowKeysByScopeMapKey.get(usageSummaryRowMapKey(row))
    if (!rowKeys) continue
    for (const rowKey of rowKeys) {
      result.set(rowKey, usageSummaryFromAggregate(row))
    }
  }
  return result
}

function loadUsageRangeSummariesForScopeRequests(scopes: UsageSummaryScopeRequest[], scopeType: UsageSummaryScopeType, range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): Map<string, AccountUsageSummary> {
  const validScopes = scopes.filter((scope) => scope.rowKey && scope.systemAccountId && scope.scopeId)
  const result = new Map<string, AccountUsageSummary>()
  const rowKeysByScopeMapKey = new Map<string, Set<string>>()
  const scopeRowsByMapKey = new Map<string, UsageSummaryScopeRequest>()
  for (const scope of validScopes) {
    const mapKey = usageSummaryScopeMapKey(scope)
    scopeRowsByMapKey.set(mapKey, scope)
    const rowKeys = rowKeysByScopeMapKey.get(mapKey) ?? new Set<string>()
    rowKeys.add(scope.rowKey)
    rowKeysByScopeMapKey.set(mapKey, rowKeys)
  }
  const normalizedScopes = [...scopeRowsByMapKey.values()]
  if (!normalizedScopes.length) return result

  const aggregateByScopeMapKey = new Map<string, AccountUsageSummary>()
  const bucketSources = usageRangeBucketSources(range)
  if (!bucketSources.length) return result

  const database = getDatabase()
  const scopesBySystemAccountId = new Map<string, UsageSummaryScopeRequest[]>()
  for (const scope of normalizedScopes) {
    scopesBySystemAccountId.set(scope.systemAccountId, [...(scopesBySystemAccountId.get(scope.systemAccountId) ?? []), scope])
  }

  for (const [systemAccountId, systemScopes] of scopesBySystemAccountId) {
    const scopeIds = [...new Set(systemScopes.map((scope) => scope.scopeId))]
    for (const scopeIdChunk of chunkValues(scopeIds, 400)) {
      for (const source of bucketSources) {
        const rows = database.prepare(`
          SELECT
            system_account_id,
            scope_id,
            COALESCE(SUM(request_count), 0) AS request_count,
            COALESCE(SUM(input_tokens), 0) AS input_tokens,
            COALESCE(SUM(output_tokens), 0) AS output_tokens,
            COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
            COALESCE(SUM(total_cost_usd), 0) AS total_cost,
            MAX(last_used_at) AS last_used_at
          FROM ${source.tableName}
          WHERE system_account_id = ?
            AND scope_type = ?
            AND ${source.timeColumn} IN (${sqlPlaceholders(source.timeValues.length)})
            AND scope_id IN (${sqlPlaceholders(scopeIdChunk.length)})
          GROUP BY system_account_id, scope_id
        `).all(systemAccountId, scopeType, ...source.timeValues, ...scopeIdChunk) as unknown as UsageSummaryAggregateRow[]
        for (const row of rows) {
          const mapKey = usageSummaryRowMapKey(row)
          aggregateByScopeMapKey.set(mapKey, addUsageSummaries(aggregateByScopeMapKey.get(mapKey), usageSummaryFromAggregate(row)))
        }
      }
    }
  }

  for (const [mapKey, summary] of aggregateByScopeMapKey) {
    const rowKeys = rowKeysByScopeMapKey.get(mapKey)
    if (!rowKeys) continue
    for (const rowKey of rowKeys) {
      result.set(rowKey, summary)
    }
  }
  return result
}

export function loadUsageRangeSummaryForScope(input: {
  systemAccountId: string
  scopeType: UsageSummaryScopeType
  scopeId: string
  range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>
}): AccountUsageSummary {
  return loadUsageRangeSummariesForScopeRequests([{
    rowKey: input.scopeId,
    systemAccountId: input.systemAccountId,
    scopeId: input.scopeId
  }], input.scopeType, input.range).get(input.scopeId) ?? emptyAccountUsageSummary()
}

function usageRangeBucketSources(range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): UsageRangeBucketSource[] {
  const plan = usageStatsBucketPlan(range)
  const sources: UsageRangeBucketSource[] = [
    { tableName: 'usage_stats_monthly', timeColumn: 'stat_month', timeValues: plan.monthly },
    { tableName: 'usage_stats_weekly', timeColumn: 'stat_week', timeValues: plan.weekly },
    { tableName: 'usage_stats_daily', timeColumn: 'stat_date', timeValues: plan.daily }
  ]
  return sources.filter((source) => source.timeValues.length > 0)
}

export function loadAccountUsageSummariesForScopes(scopes: UsageSummaryScopeRequest[], statDate?: string): Map<string, AccountUsageSummary> {
  return loadUsageSummariesForScopeRequests(scopes, 'account', statDate)
}

export function loadGroupUsageSummariesForScopes(scopes: UsageSummaryScopeRequest[], statDate?: string): Map<string, AccountUsageSummary> {
  return loadUsageSummariesForScopeRequests(scopes, 'group', statDate)
}

export function loadApiKeyUsageSummariesForScopes(scopes: UsageSummaryScopeRequest[], statDate?: string): Map<string, AccountUsageSummary> {
  return loadUsageSummariesForScopeRequests(scopes, 'api_key', statDate)
}

export function loadAuthorizationUsageSummariesForScopes(scopes: UsageSummaryScopeRequest[], scopeType: AuthorizationUsageScopeType, statDate?: string): Map<string, AccountUsageSummary> {
  return loadUsageSummariesForScopeRequests(scopes, scopeType, statDate)
}

export function loadAuthorizationUsageRangeSummariesForScopes(scopes: UsageSummaryScopeRequest[], scopeType: AuthorizationUsageScopeType, range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): Map<string, AccountUsageSummary> {
  return loadUsageRangeSummariesForScopeRequests(scopes, scopeType, range)
}
