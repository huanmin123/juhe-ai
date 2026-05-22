import { getDatabase, getStatsDatabase, nowIso } from './database.js'
import { createAppCache } from '../shared/cache.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { clearUsageStatsTimezoneCache, normalizeUsageStatsTimezone, usageStatsTimezone } from './usage-stats-helpers.js'

interface GlobalSettingRow {
  key: string
  value_json: string
  updated_at: string
}

const SYSTEM_SETTINGS_ACCOUNT_ID = 'sys_admin'
const settingsCacheTtlMs = 60_000
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
  'tableMonitorMaxTablesPerRun',
  'accountQualityRefreshIntervalSeconds',
  'accountQualityWindowMinutes',
  'cooldownAccountRetestIntervalSeconds',
  'cooldownAccountRetestBatchSize',
  'cooldownAccountRetestModel',
  'cooldownAccountRetestMaxBackoffHours',
  'oauthAccessTokenRefreshIntervalSeconds',
  'oauthAccessTokenRefreshLeadSeconds',
  'oauthAccessTokenRefreshBatchSize',
  'oauthAccessTokenRefreshRetryBackoffSeconds',
  'modelCheckRetentionDays',
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
const GLOBAL_SETTING_KEYS = new Set(['appName', 'appIcon'])
const systemSettingsCache = createAppCache<string, Record<string, unknown>>({
  name: 'settings:system',
  max: 1,
  ttlMs: settingsCacheTtlMs
})
const globalSettingsCache = createAppCache<string, Record<string, unknown>>({
  name: 'settings:global',
  max: 1,
  ttlMs: settingsCacheTtlMs
})

export function listGlobalSettings(): Record<string, unknown> {
  const cached = globalSettingsCache.get('current')
  if (cached) {
    return { ...cached }
  }
  const rows = getDatabase().prepare("SELECT key, value_json, updated_at FROM global_settings WHERE key IN ('appName', 'appIcon') ORDER BY key ASC").all() as unknown as Array<GlobalSettingRow>
  const settings = Object.fromEntries(rows.map((row) => {
    const value = JSON.parse(row.value_json) as unknown
    return [row.key, value]
  }))
  globalSettingsCache.set('current', settings)
  return { ...settings }
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
  clearGlobalSettingsCache()
  return listGlobalSettings()
}

function pickGlobalSettings(input: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(input)
    .filter(([key]) => GLOBAL_SETTING_KEYS.has(key)))
}

export function getSettings(): Record<string, unknown> {
  const cached = systemSettingsCache.get('current')
  if (cached) {
    return { ...cached }
  }
  const systemAccountId = SYSTEM_SETTINGS_ACCOUNT_ID
  const rows = getDatabase().prepare('SELECT key, value_json FROM system_settings WHERE system_account_id = ? ORDER BY key ASC').all(systemAccountId) as Array<{ key: string; value_json: string }>
  const settings = Object.fromEntries(rows.filter((row) => isSystemSettingKey(row.key)).map((row) => [row.key, JSON.parse(row.value_json) as unknown]))
  systemSettingsCache.set('current', settings)
  return { ...settings }
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
  clearSystemSettingsCache()
  notifyGatewayRuntimeCacheInvalidation('settings_updated')
  return getSettings()
}

export function clearSettingsRepositoryCache(): void {
  clearSystemSettingsCache()
  clearGlobalSettingsCache()
}

function isSystemSettingKey(key: string): boolean {
  return SYSTEM_SETTING_KEYS.has(key)
}

function clearSystemSettingsCache(): void {
  systemSettingsCache.clear()
  clearUsageStatsTimezoneCache()
}

function clearGlobalSettingsCache(): void {
  globalSettingsCache.clear()
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
  const database = getStatsDatabase()
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
