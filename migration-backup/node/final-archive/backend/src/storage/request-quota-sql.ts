import type { DatabaseSync } from 'node:sqlite'

import { getStatsDatabase, statsDatabasePath } from './database.js'
import { dateKey, monthKey, usageStatsTimezone, weekKey } from './usage-stats-helpers.js'

export const requestQuotaDatabaseAlias = 'request_quota_records'

export type RequestQuotaSqlParam = string | number

export interface RequestQuotaSqlExpression {
  sql: string
  params: RequestQuotaSqlParam[]
}

export type RequestQuotaSqlScopeType =
  | 'account_authorization'
  | 'account_authorization_team'
  | 'group_authorization'
  | 'group_authorization_team'
  | 'api_key'

export function ensureRequestQuotaDatabaseAttached(database: DatabaseSync): void {
  getStatsDatabase()
  const rows = database.prepare('PRAGMA database_list').all() as unknown as Array<{ name?: string }>
  if (rows.some((row) => row.name === requestQuotaDatabaseAlias)) return
  database.prepare(`ATTACH DATABASE ? AS ${requestQuotaDatabaseAlias}`).run(statsDatabasePath())
}

export function requestQuotaExceededSql(input: {
  limitsSql: string
  systemAccountSql: string
  scopeType: RequestQuotaSqlScopeType
  scopeIdSql: string
  now?: Date
}): RequestQuotaSqlExpression {
  const now = input.now ?? new Date()
  const timezone = usageStatsTimezone()
  const statDate = dateKey(now, timezone)
  const statWeek = weekKey(now, timezone)
  const statMonth = monthKey(now, timezone)
  const params: RequestQuotaSqlParam[] = []

  const totalCostSql = costLookupSql({
    tableName: 'usage_stats_totals',
    systemAccountSql: input.systemAccountSql,
    scopeType: input.scopeType,
    scopeIdSql: input.scopeIdSql
  })
  const hourlyCostSql = costLookupSql({
    tableName: 'usage_quota_hourly_windows',
    systemAccountSql: input.systemAccountSql,
    scopeType: input.scopeType,
    scopeIdSql: input.scopeIdSql,
    extraClause: `AND window_hours = CAST(json_extract(${input.limitsSql}, '$.hourly.hours') AS INTEGER)`
  })
  const dailyCostSql = costLookupSql({
    tableName: 'usage_stats_daily',
    systemAccountSql: input.systemAccountSql,
    scopeType: input.scopeType,
    scopeIdSql: input.scopeIdSql,
    extraClause: 'AND stat_date = ?'
  })
  params.push(statDate)
  const weeklyCostSql = costLookupSql({
    tableName: 'usage_stats_weekly',
    systemAccountSql: input.systemAccountSql,
    scopeType: input.scopeType,
    scopeIdSql: input.scopeIdSql,
    extraClause: 'AND stat_week = ?'
  })
  params.push(statWeek)
  const monthlyCostSql = costLookupSql({
    tableName: 'usage_stats_monthly',
    systemAccountSql: input.systemAccountSql,
    scopeType: input.scopeType,
    scopeIdSql: input.scopeIdSql,
    extraClause: 'AND stat_month = ?'
  })
  params.push(statMonth)

  return {
    sql: `(${input.limitsSql} IS NOT NULL
      AND json_valid(${input.limitsSql})
      AND (
        (json_extract(${input.limitsSql}, '$.hourly.enabled') = 1
          AND ${hourlyCostSql} >= CAST(json_extract(${input.limitsSql}, '$.hourly.limit') AS REAL))
        OR (json_extract(${input.limitsSql}, '$.daily.enabled') = 1
          AND ${dailyCostSql} >= CAST(json_extract(${input.limitsSql}, '$.daily.limit') AS REAL))
        OR (json_extract(${input.limitsSql}, '$.weekly.enabled') = 1
          AND ${weeklyCostSql} >= CAST(json_extract(${input.limitsSql}, '$.weekly.limit') AS REAL))
        OR (json_extract(${input.limitsSql}, '$.monthly.enabled') = 1
          AND ${monthlyCostSql} >= CAST(json_extract(${input.limitsSql}, '$.monthly.limit') AS REAL))
        OR (json_extract(${input.limitsSql}, '$.total.enabled') = 1
          AND ${totalCostSql} >= CAST(json_extract(${input.limitsSql}, '$.total.limit') AS REAL))
      ))`,
    params
  }
}

function costLookupSql(input: {
  tableName: 'usage_stats_totals' | 'usage_quota_hourly_windows' | 'usage_stats_daily' | 'usage_stats_weekly' | 'usage_stats_monthly'
  systemAccountSql: string
  scopeType: RequestQuotaSqlScopeType
  scopeIdSql: string
  extraClause?: string
}): string {
  return `COALESCE((
    SELECT total_cost_usd
    FROM ${requestQuotaDatabaseAlias}.${input.tableName}
    WHERE system_account_id = ${input.systemAccountSql}
      AND scope_type = '${input.scopeType}'
      AND scope_id = ${input.scopeIdSql}
      ${input.extraClause ?? ''}
    LIMIT 1
  ), 0)`
}
