import { getDatabase, getRecordDatabase, nowIso } from './database.js'
import { normalizeUsageStatsTimezone, usageStatsTimezone } from './usage-stats-helpers.js'

interface GlobalSettingRow {
  key: string
  value_json: string
  updated_at: string
}

const SYSTEM_SETTINGS_ACCOUNT_ID = 'sys_admin'
export const systemSettingKeys = [
  'defaultTemporaryUnschedulableMinutes',
  'temporaryUnschedulableRetryIntervalSeconds',
  'temporaryUnschedulableRetryAttempts',
  'streamCircuitBreakerEnabled',
  'streamRequestTimeoutSeconds',
  'streamIdleTimeoutSeconds',
  'streamFailureThresholdCount',
  'streamFailureThresholdWindowMinutes',
  'operationLogEnabled',
  'operationLogRetentionDays',
  'operationLogMaxChangesPerRecord',
  'statsAggregationIntervalSeconds',
  'statsAggregationBatchSize',
  'statsAggregationMaxBatchesPerRun',
  'groupAccountStatsRefreshIntervalSeconds',
  'systemMetricsSampleIntervalSeconds',
  'accountQualityRefreshIntervalSeconds',
  'accountQualityWindowMinutes',
  'cooldownAccountRetestEnabled',
  'cooldownAccountRetestIntervalSeconds',
  'cooldownAccountRetestBatchSize',
  'cooldownAccountRetestModel',
  'oauthAccessTokenRefreshIntervalSeconds',
  'oauthAccessTokenRefreshLeadSeconds',
  'oauthAccessTokenRefreshBatchSize',
  'oauthAccessTokenRefreshRetryBackoffSeconds',
  'usageRecordRetentionDays',
  'usageStatsTimezone',
  'usageStatsMinuteRetentionHours',
  'usageStatsHourlyRetentionDays',
  'usageStatsDailyRetentionDays',
  'usageStatsWeeklyRetentionWeeks',
  'usageStatsMonthlyRetentionMonths',
  'usageRankSnapshotRetentionDays',
  'systemMetricsRetentionDays',
  'systemMetricsHourlyRetentionDays',
  'dataRetentionCleanupBatchSize',
  'dataRetentionCleanupMaxBatchesPerRun'
] as const

const SYSTEM_SETTING_KEYS = new Set<string>(systemSettingKeys)

export function listGlobalSettings(): Record<string, unknown> {
  const rows = getDatabase().prepare('SELECT key, value_json, updated_at FROM global_settings ORDER BY key ASC').all() as unknown as Array<GlobalSettingRow>
  return Object.fromEntries(rows.map((row) => {
    const value = JSON.parse(row.value_json) as unknown
    return [row.key, normalizeGlobalSettingValue(row.key, value)]
  }))
}

export function listPublicGlobalSettings(): Record<string, unknown> {
  return pickGlobalSettings(listGlobalSettings())
}

export function updateGlobalSettings(input: Record<string, unknown>): Record<string, unknown> {
  const statement = getDatabase().prepare('INSERT INTO global_settings (key, value_json, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at')
  const now = nowIso()
  for (const [key, value] of Object.entries(pickGlobalSettings(input))) {
    statement.run(key, JSON.stringify(value), now)
  }
  return listGlobalSettings()
}

function pickGlobalSettings(input: Record<string, unknown>): Record<string, unknown> {
  const allowedKeys = new Set(['appName', 'appIcon'])
  return Object.fromEntries(Object.entries(input)
    .filter(([key]) => allowedKeys.has(key))
    .map(([key, value]) => [key, normalizeGlobalSettingValue(key, value)]))
}

function normalizeGlobalSettingValue(key: string, value: unknown): unknown {
  if (key === 'appIcon' && (value === '/brand-icon.svg' || value === '/__jhsys/brand-icon.svg')) {
    return '/__aisys__/brand-icon.svg'
  }
  return value
}

export function getSettings(): Record<string, unknown> {
  const systemAccountId = SYSTEM_SETTINGS_ACCOUNT_ID
  const rows = getDatabase().prepare('SELECT key, value_json FROM system_settings WHERE system_account_id = ? ORDER BY key ASC').all(systemAccountId) as Array<{ key: string; value_json: string }>
  return Object.fromEntries(rows.filter((row) => isSystemSettingKey(row.key)).map((row) => [row.key, JSON.parse(row.value_json) as unknown]))
}

export function updateSettings(input: Record<string, unknown>): Record<string, unknown> {
  const systemAccountId = SYSTEM_SETTINGS_ACCOUNT_ID
  assertUsageStatsTimezoneUpdateAllowed(input)
  const statement = getDatabase().prepare(`
    INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
    VALUES (?, ?, ?, ?)
    ON CONFLICT(system_account_id, key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at
  `)
  const now = nowIso()
  for (const [key, value] of Object.entries(input)) {
    if (!isSystemSettingKey(key)) {
      continue
    }
    statement.run(systemAccountId, key, JSON.stringify(value), now)
  }
  return getSettings()
}

function isSystemSettingKey(key: string): boolean {
  return SYSTEM_SETTING_KEYS.has(key)
}

function assertUsageStatsTimezoneUpdateAllowed(input: Record<string, unknown>): void {
  if (!Object.prototype.hasOwnProperty.call(input, 'usageStatsTimezone')) {
    return
  }
  const current = usageStatsTimezone()
  const next = normalizeUsageStatsTimezone(input.usageStatsTimezone)
  if (next === current) {
    return
  }
  if (!usageStatsDataExists()) {
    return
  }
  throw new Error('已有统计数据后不能直接修改统计时区，请先备份并重建统计缓存')
}

function usageStatsDataExists(): boolean {
  const database = getRecordDatabase()
  const tables = [
    'usage_stats_totals',
    'usage_stats_minute',
    'usage_stats_hourly',
    'usage_stats_daily',
    'usage_stats_weekly',
    'usage_stats_monthly',
    'authorization_team_usage_summary_daily',
    'authorization_team_usage_range_windows',
    'authorization_user_usage_summary_daily',
    'authorization_user_usage_range_windows',
    'usage_overview_summary_windows',
    'usage_overview_trend_windows',
    'usage_model_rank_windows',
    'usage_error_rank_windows',
    'ai_performance_summary_windows',
    'usage_quota_hourly_windows',
    'usage_scope_range_windows',
    'system_metrics_trend_windows'
  ]
  return tables.some((tableName) => {
    const row = database.prepare(`SELECT 1 FROM ${tableName} LIMIT 1`).get() as unknown
    return Boolean(row)
  })
}
