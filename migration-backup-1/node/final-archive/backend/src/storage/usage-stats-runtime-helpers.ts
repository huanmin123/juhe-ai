import type { AccountUsageStatsRange } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { getStatsDatabase } from './database.js'
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

export function latestUsageStatsLagSeconds(): number | undefined {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('PostgreSQL 模式禁止读取 SQLite stats_job_state，请使用 latestUsageStatsLagSecondsForRuntime')
  }
  const database = getStatsDatabase()
  const shardRow = database
    .prepare("SELECT MAX(lag_seconds) AS lag_seconds FROM stats_job_state WHERE scope_type = 'usage_shard' AND job_name = 'usage_stats_aggregation'")
    .get() as unknown as { lag_seconds?: number | null } | undefined
  const shardLag = numberOrUndefined(shardRow?.lag_seconds)
  if (shardLag !== undefined) {
    return shardLag
  }
  const row = database
    .prepare("SELECT lag_seconds FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'")
    .get() as unknown as { lag_seconds?: number | null } | undefined
  return numberOrUndefined(row?.lag_seconds)
}

function numberOrUndefined(value: number | null | undefined): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}
