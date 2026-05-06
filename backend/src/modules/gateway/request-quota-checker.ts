import type { DatabaseSync } from 'node:sqlite'

import type { RequestQuotaLimits } from '../../domain/types.js'
import { dateKey, hourKey } from '../../storage/usage-stats-helpers.js'

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
  const startOfToday = new Date(input.now)
  startOfToday.setHours(0, 0, 0, 0)
  const startOfWeek = new Date(startOfToday)
  const dayOfWeek = startOfWeek.getDay()
  startOfWeek.setDate(startOfWeek.getDate() - (dayOfWeek === 0 ? 6 : dayOfWeek - 1))
  const startOfMonth = new Date(startOfToday.getFullYear(), startOfToday.getMonth(), 1)
  const dailyStatsStart = startOfWeek < startOfMonth ? startOfWeek : startOfMonth
  const hourlySince = input.hourlyWindowHours
    ? hourKey(new Date(input.now.getTime() - Math.max(1, input.hourlyWindowHours) * 60 * 60 * 1000))
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

  const dailyRows = database.prepare(`
    SELECT
      COALESCE(SUM(CASE WHEN stat_date >= ? THEN total_cost_usd ELSE 0 END), 0) AS daily,
      COALESCE(SUM(CASE WHEN stat_date >= ? THEN total_cost_usd ELSE 0 END), 0) AS weekly,
      COALESCE(SUM(CASE WHEN stat_date >= ? THEN total_cost_usd ELSE 0 END), 0) AS monthly
    FROM usage_stats_daily
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_date >= ?
  `).get(
    dateKey(startOfToday),
    dateKey(startOfWeek),
    dateKey(startOfMonth),
    input.systemAccountId,
    input.scopeType,
    input.scopeId,
    dateKey(dailyStatsStart)
  ) as unknown as { daily?: number; weekly?: number; monthly?: number } | undefined

  return {
    hourly: Number(hourlyRow?.total_cost ?? 0),
    daily: Number(dailyRows?.daily ?? 0),
    weekly: Number(dailyRows?.weekly ?? 0),
    monthly: Number(dailyRows?.monthly ?? 0),
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
