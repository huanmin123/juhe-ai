import { runtimeConfig } from '../config/runtime.js'
import type { AccountListUsageSummary } from '../domain/types.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getStatsDatabase, isSqliteDatabaseLocked, runWithSqliteBusyTimeout } from './database.js'
import { getPostgresPool } from './postgres-client.js'

export type AccountManagementListUsageScopeType = 'account' | 'account_authorization'

export interface AccountManagementListUsageScope {
  rowKey: string
  systemAccountId: string
  scopeType: AccountManagementListUsageScopeType
  scopeId: string
}

export interface AccountManagementListUsageValue extends AccountListUsageSummary {
  lastUsedAt?: string
}

interface AccountManagementListUsageRow {
  row_key: string
  request_count: number | string | null
  total_tokens: number | string | null
  total_cost: number | string | null
  last_used_at: string | null
}

const accountManagementListStatsBusyTimeoutMs = 60

export function emptyAccountManagementListUsage(): AccountListUsageSummary {
  return { requestCount: 0, totalTokens: 0, totalCost: 0 }
}

export async function loadAccountManagementListUsageAsync(
  scopes: AccountManagementListUsageScope[],
  statDate?: string
): Promise<Map<string, AccountManagementListUsageValue>> {
  const normalized = uniqueScopes(scopes)
  if (!normalized.length) return new Map()
  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    return usageMap(await client.query<AccountManagementListUsageRow>(
      usageSql(client.dialect.qualifyTable('juhe_stats', statDate ? 'usage_stats_daily' : 'usage_stats_totals'), normalized.length, Boolean(statDate)),
      usageParams(normalized, statDate)
    ))
  }
  const tableName = statDate ? 'usage_stats_daily' : 'usage_stats_totals'
  const database = getStatsDatabase()
  try {
    return runWithSqliteBusyTimeout(database, accountManagementListStatsBusyTimeoutMs, () => {
      const rows = database.prepare(usageSql(tableName, normalized.length, Boolean(statDate)))
        .all(...usageParams(normalized, statDate)) as unknown as AccountManagementListUsageRow[]
      return usageMap(rows)
    })
  } catch (error) {
    if (!isSqliteDatabaseLocked(error)) throw error
    const wrapped = new Error(`AI 账户列表统计读取遇到 SQLite 忙锁：${statDate ? 'today_usage' : 'total_usage'}`) as Error & {
      code?: unknown
      errcode?: unknown
      errstr?: unknown
    }
    const source = error as Error & { code?: unknown; errcode?: unknown; errstr?: unknown }
    wrapped.code = source.code
    wrapped.errcode = source.errcode
    wrapped.errstr = source.errstr
    throw wrapped
  }
}

function uniqueScopes(scopes: AccountManagementListUsageScope[]): AccountManagementListUsageScope[] {
  const output = new Map<string, AccountManagementListUsageScope>()
  for (const scope of scopes) {
    if (!scope.rowKey || !scope.systemAccountId || !scope.scopeId) continue
    output.set(`${scope.rowKey}\u0000${scope.systemAccountId}\u0000${scope.scopeType}\u0000${scope.scopeId}`, scope)
  }
  return [...output.values()]
}

function usageSql(tableName: string, scopeCount: number, includeDate: boolean): string {
  const requestedRows = Array.from({ length: scopeCount }, () => '(?, ?, ?, ?)').join(', ')
  return `
    WITH requested(row_key, system_account_id, scope_type, scope_id) AS (
      VALUES ${requestedRows}
    )
    SELECT
      requested.row_key,
      COALESCE(usage_rows.request_count, 0) AS request_count,
      COALESCE(usage_rows.input_tokens, 0) + COALESCE(usage_rows.output_tokens, 0) AS total_tokens,
      COALESCE(usage_rows.total_cost_usd, 0) AS total_cost,
      usage_rows.last_used_at
    FROM requested
    LEFT JOIN ${tableName} usage_rows
      ON usage_rows.system_account_id = requested.system_account_id
      AND usage_rows.scope_type = requested.scope_type
      AND usage_rows.scope_id = requested.scope_id
      ${includeDate ? 'AND usage_rows.stat_date = ?' : ''}
  `
}

function usageParams(scopes: AccountManagementListUsageScope[], statDate?: string): Array<string> {
  return [
    ...scopes.flatMap((scope) => [scope.rowKey, scope.systemAccountId, scope.scopeType, scope.scopeId]),
    ...(statDate ? [statDate] : [])
  ]
}

function usageMap(rows: AccountManagementListUsageRow[]): Map<string, AccountManagementListUsageValue> {
  return new Map(rows.map((row) => [row.row_key, {
    requestCount: Number(row.request_count ?? 0),
    totalTokens: Number(row.total_tokens ?? 0),
    totalCost: Number(row.total_cost ?? 0),
    lastUsedAt: row.last_used_at ?? undefined
  }]))
}
