import type { AccountUsageStatsRange } from '../domain/types.js'
import { getRecordDatabase } from './database.js'
import { dateKey, usageStatsTimezone } from './usage-stats-helpers.js'
import { FIXED_RANGE_WINDOW_DAYS } from './usage-stats-window-helpers.js'

export function normalizeDefaultUsageStatsRange(timezone = usageStatsTimezone()): AccountUsageStatsRange {
  const today = dateKey(new Date(), timezone)
  return {
    startDate: today,
    endDate: today,
    days: 1,
    maxDays: FIXED_RANGE_WINDOW_DAYS
  }
}

export function latestUsageStatsLagSeconds(): number {
  const row = getRecordDatabase()
    .prepare("SELECT lag_seconds FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'")
    .get() as unknown as { lag_seconds?: number } | undefined
  return Number(row?.lag_seconds ?? 0)
}
