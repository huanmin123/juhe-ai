import type { DatabaseSync } from 'node:sqlite'

import type { RequestQuotaLimits } from '../../domain/types.js'
import { dateKey, hourKey } from '../../storage/usage-stats-helpers.js'

export interface RequestQuotaCounts {
  hourly: number
  daily: number
  weekly: number
  monthly: number
  total: number
}

export function loadRequestQuotaCounts(database: DatabaseSync, input: {
  systemAccountId: string
  scopeType: string
  scopeId: string
  now: Date
  hourlyWindowHours?: number
}): RequestQuotaCounts {
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
    SELECT COALESCE(request_count, 0) AS request_count
    FROM usage_stats_totals
    WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
  `).get(input.systemAccountId, input.scopeType, input.scopeId) as unknown as { request_count?: number } | undefined

  const hourlyRow = hourlySince
    ? database.prepare(`
      SELECT COALESCE(SUM(request_count), 0) AS request_count
      FROM usage_stats_hourly
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ? AND stat_hour >= ?
    `).get(input.systemAccountId, input.scopeType, input.scopeId, hourlySince) as unknown as { request_count?: number } | undefined
    : undefined

  const dailyRows = database.prepare(`
    SELECT
      COALESCE(SUM(CASE WHEN stat_date >= ? THEN request_count ELSE 0 END), 0) AS daily,
      COALESCE(SUM(CASE WHEN stat_date >= ? THEN request_count ELSE 0 END), 0) AS weekly,
      COALESCE(SUM(CASE WHEN stat_date >= ? THEN request_count ELSE 0 END), 0) AS monthly
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
    hourly: Number(hourlyRow?.request_count ?? 0),
    daily: Number(dailyRows?.daily ?? 0),
    weekly: Number(dailyRows?.weekly ?? 0),
    monthly: Number(dailyRows?.monthly ?? 0),
    total: Number(totalRow?.request_count ?? 0)
  }
}

export function isRequestQuotaExceeded(limits: RequestQuotaLimits, counts: RequestQuotaCounts): boolean {
  return Boolean(
    (limits.hourly?.enabled && counts.hourly >= limits.hourly.limit)
    || (limits.daily?.enabled && counts.daily >= limits.daily.limit)
    || (limits.weekly?.enabled && counts.weekly >= limits.weekly.limit)
    || (limits.monthly?.enabled && counts.monthly >= limits.monthly.limit)
    || (limits.total?.enabled && counts.total >= limits.total.limit)
  )
}
