import type { DatabaseSync } from 'node:sqlite'

import type { RequestQuotaLimits } from '../../domain/types.js'
import { dateKey, hourKey, monthKey, usageStatsTimezone, weekKey } from '../../storage/usage-stats-helpers.js'

export interface RequestQuotaCosts {
  hourly: number
  daily: number
  weekly: number
  monthly: number
  total: number
}

export function loadRequestQuotaCosts(database: DatabaseSync, input: {
  systemAccountId: string
  scopeType: string
  scopeId: string
  now: Date
  hourlyWindowHours?: number
}): RequestQuotaCosts {
  const timezone = usageStatsTimezone(database)
  const hourlySince = input.hourlyWindowHours
    ? hourKey(new Date(input.now.getTime() - Math.max(1, input.hourlyWindowHours) * 60 * 60 * 1000), timezone)
    : undefined

  const totalRow = database.prepare(`
    SELECT COALESCE(total_cost_usd, 0) AS total_cost
    FROM usage_stats_totals
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
  `).get(input.systemAccountId, input.scopeType, input.scopeId) as unknown as { total_cost?: number } | undefined

  const hourlyRow = hourlySince
    ? database.prepare(`
      SELECT COALESCE(SUM(total_cost_usd), 0) AS total_cost
      FROM usage_stats_hourly
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_hour >= ?
    `).get(input.systemAccountId, input.scopeType, input.scopeId, hourlySince) as unknown as { total_cost?: number } | undefined
    : undefined

  const dailyRow = database.prepare(`
    SELECT COALESCE(total_cost_usd, 0) AS total_cost
    FROM usage_stats_daily
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_date = ?
  `).get(input.systemAccountId, input.scopeType, input.scopeId, dateKey(input.now, timezone)) as unknown as { total_cost?: number } | undefined

  const weeklyRow = database.prepare(`
    SELECT COALESCE(total_cost_usd, 0) AS total_cost
    FROM usage_stats_weekly
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_week = ?
  `).get(input.systemAccountId, input.scopeType, input.scopeId, weekKey(input.now, timezone)) as unknown as { total_cost?: number } | undefined

  const monthlyRow = database.prepare(`
    SELECT COALESCE(total_cost_usd, 0) AS total_cost
    FROM usage_stats_monthly
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_month = ?
  `).get(input.systemAccountId, input.scopeType, input.scopeId, monthKey(input.now, timezone)) as unknown as { total_cost?: number } | undefined

  return {
    hourly: Number(hourlyRow?.total_cost ?? 0),
    daily: Number(dailyRow?.total_cost ?? 0),
    weekly: Number(weeklyRow?.total_cost ?? 0),
    monthly: Number(monthlyRow?.total_cost ?? 0),
    total: Number(totalRow?.total_cost ?? 0)
  }
}

export function isRequestQuotaExceeded(limits: RequestQuotaLimits, costs: RequestQuotaCosts): boolean {
  return Boolean(
    (limits.hourly?.enabled && costs.hourly >= limits.hourly.limit)
    || (limits.daily?.enabled && costs.daily >= limits.daily.limit)
    || (limits.weekly?.enabled && costs.weekly >= limits.weekly.limit)
    || (limits.monthly?.enabled && costs.monthly >= limits.monthly.limit)
    || (limits.total?.enabled && costs.total >= limits.total.limit)
  )
}
