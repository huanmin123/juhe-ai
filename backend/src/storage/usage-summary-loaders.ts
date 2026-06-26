import type { AccountUsageStatsRange, AccountUsageSummary } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { getStatsDatabase } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { emptyAccountUsageSummary, usageSummaryFromAggregate } from './usage-stats-helpers.js'

type AuthorizationUsageScopeType = 'account_authorization' | 'group_authorization' | 'account_authorization_team' | 'group_authorization_team'
type UsageSummaryScopeType = 'account' | 'group' | 'api_key' | AuthorizationUsageScopeType
const statsSchemaName = 'juhe_stats'

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
  cache_read_cost_usd: number
  total_cost: number
  last_used_at: string | null
}

function usageStatsSource(statDate?: string): { tableName: 'usage_stats_daily' | 'usage_stats_totals'; dateClause: string } {
  return statDate
    ? { tableName: 'usage_stats_daily', dateClause: ' AND stat_date = ?' }
    : { tableName: 'usage_stats_totals', dateClause: '' }
}

function usageStatsSourceForClient(client: DatabaseClient, statDate?: string): { tableName: string; dateClause: string } {
  const source = usageStatsSource(statDate)
  return {
    tableName: statsTable(client, source.tableName),
    dateClause: source.dateClause
  }
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
  const database = getStatsDatabase()
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
          cache_read_cost_usd,
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

async function loadUsageSummariesForScopeRequestsAsync(scopes: UsageSummaryScopeRequest[], scopeType: UsageSummaryScopeType, statDate?: string): Promise<Map<string, AccountUsageSummary>> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return loadUsageSummariesForScopeRequests(scopes, scopeType, statDate)
  }
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

  const client = await getUsageSummaryDatabaseClient()
  const source = usageStatsSourceForClient(client, statDate)
  const scopesBySystemAccountId = new Map<string, UsageSummaryScopeRequest[]>()
  for (const scope of normalizedScopes) {
    scopesBySystemAccountId.set(scope.systemAccountId, [...(scopesBySystemAccountId.get(scope.systemAccountId) ?? []), scope])
  }

  for (const [systemAccountId, systemScopes] of scopesBySystemAccountId) {
    const scopeIds = [...new Set(systemScopes.map((scope) => scope.scopeId))]
    for (const scopeIdChunk of chunkValues(scopeIds, 400)) {
      const rows = await client.query<UsageSummaryAggregateRow>(`
        SELECT
          system_account_id,
          scope_id,
          request_count,
          input_tokens,
          output_tokens,
          cache_read_tokens,
          cache_read_cost_usd,
          total_cost_usd AS total_cost,
          last_used_at
        FROM ${source.tableName}
        WHERE system_account_id = ?
          AND scope_type = ?${source.dateClause}
          AND scope_id IN (${sqlPlaceholders(scopeIdChunk.length)})
      `, statDate
        ? [systemAccountId, scopeType, statDate, ...scopeIdChunk]
        : [systemAccountId, scopeType, ...scopeIdChunk]
      )
      for (const row of rows) {
        const rowKeys = rowKeysByScopeMapKey.get(usageSummaryRowMapKey(row))
        if (!rowKeys) continue
        for (const rowKey of rowKeys) {
          result.set(rowKey, usageSummaryFromAggregate(row))
        }
      }
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

  const database = getStatsDatabase()
  const scopesBySystemAccountId = new Map<string, UsageSummaryScopeRequest[]>()
  for (const scope of normalizedScopes) {
    scopesBySystemAccountId.set(scope.systemAccountId, [...(scopesBySystemAccountId.get(scope.systemAccountId) ?? []), scope])
  }

  for (const [systemAccountId, systemScopes] of scopesBySystemAccountId) {
    const scopeIds = [...new Set(systemScopes.map((scope) => scope.scopeId))]
    for (const scopeIdChunk of chunkValues(scopeIds, 400)) {
      const rows = database.prepare(`
        SELECT
          system_account_id,
          scope_id,
          request_count,
          input_tokens,
          output_tokens,
          cache_read_tokens,
          cache_read_cost_usd,
          total_cost_usd AS total_cost,
          last_used_at
        FROM usage_scope_range_windows
        WHERE system_account_id = ?
          AND scope_type = ?
          AND start_date = ?
          AND end_date = ?
          AND scope_id IN (${sqlPlaceholders(scopeIdChunk.length)})
      `).all(systemAccountId, scopeType, range.startDate, range.endDate, ...scopeIdChunk) as unknown as UsageSummaryAggregateRow[]
      for (const row of rows) {
        const rowKeys = rowKeysByScopeMapKey.get(usageSummaryRowMapKey(row))
        if (!rowKeys) continue
        for (const rowKey of rowKeys) {
          result.set(rowKey, usageSummaryFromAggregate(row))
        }
      }
    }
  }
  return result
}

async function loadUsageRangeSummariesForScopeRequestsAsync(scopes: UsageSummaryScopeRequest[], scopeType: UsageSummaryScopeType, range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): Promise<Map<string, AccountUsageSummary>> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return loadUsageRangeSummariesForScopeRequests(scopes, scopeType, range)
  }
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

  const client = await getUsageSummaryDatabaseClient()
  const tableName = statsTable(client, 'usage_scope_range_windows')
  const scopesBySystemAccountId = new Map<string, UsageSummaryScopeRequest[]>()
  for (const scope of normalizedScopes) {
    scopesBySystemAccountId.set(scope.systemAccountId, [...(scopesBySystemAccountId.get(scope.systemAccountId) ?? []), scope])
  }

  for (const [systemAccountId, systemScopes] of scopesBySystemAccountId) {
    const scopeIds = [...new Set(systemScopes.map((scope) => scope.scopeId))]
    for (const scopeIdChunk of chunkValues(scopeIds, 400)) {
      const rows = await client.query<UsageSummaryAggregateRow>(`
        SELECT
          system_account_id,
          scope_id,
          request_count,
          input_tokens,
          output_tokens,
          cache_read_tokens,
          cache_read_cost_usd,
          total_cost_usd AS total_cost,
          last_used_at
        FROM ${tableName}
        WHERE system_account_id = ?
          AND scope_type = ?
          AND start_date = ?
          AND end_date = ?
          AND scope_id IN (${sqlPlaceholders(scopeIdChunk.length)})
      `, [systemAccountId, scopeType, range.startDate, range.endDate, ...scopeIdChunk])
      for (const row of rows) {
        const rowKeys = rowKeysByScopeMapKey.get(usageSummaryRowMapKey(row))
        if (!rowKeys) continue
        for (const rowKey of rowKeys) {
          result.set(rowKey, usageSummaryFromAggregate(row))
        }
      }
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

export async function loadUsageRangeSummaryForScopeAsync(input: {
  systemAccountId: string
  scopeType: UsageSummaryScopeType
  scopeId: string
  range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>
}): Promise<AccountUsageSummary> {
  return (await loadUsageRangeSummariesForScopeRequestsAsync([{
    rowKey: input.scopeId,
    systemAccountId: input.systemAccountId,
    scopeId: input.scopeId
  }], input.scopeType, input.range)).get(input.scopeId) ?? emptyAccountUsageSummary()
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

export async function loadAuthorizationUsageSummariesForScopesAsync(scopes: UsageSummaryScopeRequest[], scopeType: AuthorizationUsageScopeType, statDate?: string): Promise<Map<string, AccountUsageSummary>> {
  return loadUsageSummariesForScopeRequestsAsync(scopes, scopeType, statDate)
}

export async function loadAuthorizationUsageRangeSummariesForScopesAsync(scopes: UsageSummaryScopeRequest[], scopeType: AuthorizationUsageScopeType, range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): Promise<Map<string, AccountUsageSummary>> {
  return loadUsageRangeSummariesForScopeRequestsAsync(scopes, scopeType, range)
}

async function getUsageSummaryDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getStatsDatabase())
}

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(statsSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}
