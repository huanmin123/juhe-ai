import { dateKey, hourKey, minuteKey, monthKey, usageStatsTimezone, weekKey } from './usage-stats-helpers.js'
import type { UsageStatsRecordRow } from './usage-stats-types.js'

export interface UsageStatsTimeKeys {
  statMinute: string
  statHour: string
  statDate: string
  statWeek: string
  statMonth: string
}

export interface UsageStatsTimeBucketDefinition {
  tableName: string
  columnName: string
  valueKey: keyof UsageStatsTimeKeys
}

export const usageStatsTimeBuckets: UsageStatsTimeBucketDefinition[] = [
  { tableName: 'usage_stats_minute', columnName: 'stat_minute', valueKey: 'statMinute' },
  { tableName: 'usage_stats_hourly', columnName: 'stat_hour', valueKey: 'statHour' },
  { tableName: 'usage_stats_daily', columnName: 'stat_date', valueKey: 'statDate' },
  { tableName: 'usage_stats_weekly', columnName: 'stat_week', valueKey: 'statWeek' },
  { tableName: 'usage_stats_monthly', columnName: 'stat_month', valueKey: 'statMonth' }
]

export const usageModelTimeBuckets: UsageStatsTimeBucketDefinition[] = [
  { tableName: 'usage_model_minute', columnName: 'stat_minute', valueKey: 'statMinute' },
  { tableName: 'usage_model_hourly', columnName: 'stat_hour', valueKey: 'statHour' },
  { tableName: 'usage_model_daily', columnName: 'stat_date', valueKey: 'statDate' },
  { tableName: 'usage_model_weekly', columnName: 'stat_week', valueKey: 'statWeek' },
  { tableName: 'usage_model_monthly', columnName: 'stat_month', valueKey: 'statMonth' }
]

export const usageErrorTimeBuckets: UsageStatsTimeBucketDefinition[] = [
  { tableName: 'usage_error_minute', columnName: 'stat_minute', valueKey: 'statMinute' },
  { tableName: 'usage_error_hourly', columnName: 'stat_hour', valueKey: 'statHour' },
  { tableName: 'usage_error_daily', columnName: 'stat_date', valueKey: 'statDate' },
  { tableName: 'usage_error_weekly', columnName: 'stat_week', valueKey: 'statWeek' },
  { tableName: 'usage_error_monthly', columnName: 'stat_month', valueKey: 'statMonth' }
]

export const usageLatencyTimeBuckets: UsageStatsTimeBucketDefinition[] = [
  { tableName: 'usage_latency_minute', columnName: 'stat_minute', valueKey: 'statMinute' },
  { tableName: 'usage_latency_hourly', columnName: 'stat_hour', valueKey: 'statHour' },
  { tableName: 'usage_latency_daily', columnName: 'stat_date', valueKey: 'statDate' },
  { tableName: 'usage_latency_weekly', columnName: 'stat_week', valueKey: 'statWeek' },
  { tableName: 'usage_latency_monthly', columnName: 'stat_month', valueKey: 'statMonth' }
]

export function usageStatsTimeKeys(row: UsageStatsRecordRow): UsageStatsTimeKeys {
  const createdAt = new Date(row.created_at)
  const timezone = usageStatsTimezone()
  return {
    statMinute: minuteKey(createdAt, timezone),
    statHour: hourKey(createdAt, timezone),
    statDate: dateKey(createdAt, timezone),
    statWeek: weekKey(createdAt, timezone),
    statMonth: monthKey(createdAt, timezone)
  }
}
