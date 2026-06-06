import { getBusinessDatabase, getStatsDatabase, nowIso } from './database.js'
import { createAppCache } from '../shared/cache.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { clearUsageStatsTimezoneCache, normalizeUsageStatsTimezone, usageStatsTimezone } from './usage-stats-helpers.js'
import { sqlPlaceholders } from './query-utils.js'

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
type SystemSettingKey = typeof systemSettingKeys[number]
type GlobalSettingKey = 'appName' | 'appIcon'
type SettingValidator = (value: unknown, key: string) => unknown

const globalSettingKeys = ['appName', 'appIcon'] as const
const GLOBAL_SETTING_KEYS = new Set<string>(globalSettingKeys)
const SYSTEM_SETTING_VALIDATORS: Record<SystemSettingKey, SettingValidator> = {
  defaultTemporaryUnschedulableMinutes: integerSetting(1, 1440),
  temporaryUnschedulableRetryIntervalSeconds: integerSetting(0, 3600),
  temporaryUnschedulableRetryAttempts: integerSetting(0, 10),
  streamCircuitBreakerEnabled: booleanSetting,
  streamRequestTimeoutSeconds: integerSetting(10, 3600),
  streamIdleTimeoutSeconds: integerSetting(1, 3600),
  streamFailureThresholdCount: integerSetting(1, 100),
  streamFailureThresholdWindowMinutes: integerSetting(1, 1440),
  operationLogEnabled: booleanSetting,
  operationLogRetentionDays: integerSetting(1, 3650),
  operationLogMaxChangesPerRecord: integerSetting(1, 500),
  statsAggregationIntervalSeconds: integerSetting(5, 3600),
  statsAggregationBatchSize: integerSetting(100, 10000),
  statsAggregationMaxBatchesPerRun: integerSetting(1, 100),
  groupAccountStatsRefreshIntervalSeconds: integerSetting(5, 3600),
  systemMetricsSampleIntervalSeconds: integerSetting(5, 3600),
  tableMonitorMaxTablesPerRun: integerSetting(0, 100),
  accountQualityRefreshIntervalSeconds: integerSetting(60, 3600),
  accountQualityWindowMinutes: integerSetting(1, 60),
  cooldownAccountRetestIntervalSeconds: integerSetting(1, 3600),
  cooldownAccountRetestBatchSize: integerSetting(1, 100),
  cooldownAccountRetestMaxBackoffHours: integerSetting(1, 720),
  oauthAccessTokenRefreshIntervalSeconds: integerSetting(10, 3600),
  oauthAccessTokenRefreshLeadSeconds: integerSetting(60, 86400),
  oauthAccessTokenRefreshBatchSize: integerSetting(1, 200),
  oauthAccessTokenRefreshRetryBackoffSeconds: integerSetting(0, 86400),
  modelCheckRetentionDays: integerSetting(1, 365),
  usageRecordRetentionDays: integerSetting(1, 7),
  usageStatsTimezone: timezoneSetting,
  usageStatsMinuteRetentionHours: integerSetting(1, 24 * 14),
  usageStatsHourlyRetentionDays: integerSetting(1, 180),
  usageStatsDailyRetentionDays: integerSetting(1, 800),
  usageStatsWeeklyRetentionWeeks: integerSetting(1, 260),
  usageStatsMonthlyRetentionMonths: integerSetting(1, 60),
  usageRankSnapshotRetentionDays: integerSetting(1, 365),
  systemMetricsRetentionDays: integerSetting(1, 7),
  systemMetricsHourlyRetentionDays: integerSetting(1, 30),
  dataRetentionCleanupBatchSize: integerSetting(100, 1000),
  dataRetentionCleanupMaxBatchesPerRun: integerSetting(1, 2)
}
const GLOBAL_SETTING_VALIDATORS: Record<GlobalSettingKey, SettingValidator> = {
  appName: nonEmptyStringSetting,
  appIcon: nonEmptyStringSetting
}
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
  const rows = getBusinessDatabase().prepare("SELECT key, value_json, updated_at FROM global_settings WHERE key IN ('appName', 'appIcon') ORDER BY key ASC").all() as unknown as Array<GlobalSettingRow>
  const settings: Record<string, unknown> = {}
  for (const row of rows) {
    settings[row.key] = normalizeGlobalSetting(row.key, JSON.parse(row.value_json) as unknown)
  }
  assertAllSettingsPresent(settings, globalSettingKeys, '全局设置')
  globalSettingsCache.set('current', settings)
  return { ...settings }
}

export function listPublicGlobalSettings(): Record<string, unknown> {
  return pickGlobalSettings(listGlobalSettings())
}

export function updateGlobalSettings(input: Record<string, unknown>): Record<string, unknown> {
  const normalizedInput = normalizeGlobalSettingsInput(input)
  const statement = getBusinessDatabase().prepare('INSERT INTO global_settings (key, value_json, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at')
  const now = nowIso()
  for (const [key, value] of Object.entries(normalizedInput)) {
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
  const rows = getBusinessDatabase()
    .prepare(`SELECT key, value_json FROM system_settings WHERE system_account_id = ? AND key IN (${sqlPlaceholders(systemSettingKeys.length)}) ORDER BY key ASC`)
    .all(systemAccountId, ...systemSettingKeys) as Array<{ key: string; value_json: string }>
  const settings: Record<string, unknown> = {}
  for (const row of rows) {
    settings[row.key] = normalizeSystemSetting(row.key, JSON.parse(row.value_json) as unknown)
  }
  assertAllSettingsPresent(settings, systemSettingKeys, '系统设置')
  systemSettingsCache.set('current', settings)
  return { ...settings }
}

export function updateSettings(input: Record<string, unknown>): Record<string, unknown> {
  const systemAccountId = SYSTEM_SETTINGS_ACCOUNT_ID
  const normalizedInput = normalizeSystemSettingsInput(input)
  assertUsageStatsTimezoneUpdateAllowed(normalizedInput)
  const statement = getBusinessDatabase().prepare(`
    INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
    VALUES (?, ?, ?, ?)
    ON CONFLICT(system_account_id, key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at
  `)
  const now = nowIso()
  for (const [key, value] of Object.entries(normalizedInput)) {
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

function isSystemSettingKey(key: string): key is SystemSettingKey {
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

function normalizeSystemSettingsInput(input: Record<string, unknown>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(input)) {
    output[key] = normalizeSystemSetting(key, value)
  }
  if (Object.keys(output).length === 0) {
    throw new Error('系统设置更新不能为空')
  }
  return output
}

function normalizeGlobalSettingsInput(input: Record<string, unknown>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(input)) {
    output[key] = normalizeGlobalSetting(key, value)
  }
  if (Object.keys(output).length === 0) {
    throw new Error('全局设置更新不能为空')
  }
  return output
}

function normalizeSystemSetting(key: string, value: unknown): unknown {
  if (!isSystemSettingKey(key)) {
    throw new Error(`未知系统设置字段：${key}`)
  }
  return SYSTEM_SETTING_VALIDATORS[key](value, key)
}

function normalizeGlobalSetting(key: string, value: unknown): unknown {
  if (!GLOBAL_SETTING_KEYS.has(key)) {
    throw new Error(`未知全局设置字段：${key}`)
  }
  return GLOBAL_SETTING_VALIDATORS[key as GlobalSettingKey](value, key)
}

function assertAllSettingsPresent(settings: Record<string, unknown>, keys: readonly string[], label: string): void {
  for (const key of keys) {
    if (!Object.prototype.hasOwnProperty.call(settings, key)) {
      throw new Error(`${label}缺少字段：${key}`)
    }
  }
}

function integerSetting(min: number, max: number): SettingValidator {
  return (value, key) => {
    if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
      throw new Error(`${key} 必须是整数`)
    }
    if (value < min || value > max) {
      throw new Error(`${key} 必须在 ${min} 到 ${max} 之间`)
    }
    return value
  }
}

function booleanSetting(value: unknown, key: string): boolean {
  if (typeof value !== 'boolean') {
    throw new Error(`${key} 必须是布尔值`)
  }
  return value
}

function nonEmptyStringSetting(value: unknown, key: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${key} 必须是非空字符串`)
  }
  return value.trim()
}

function timezoneSetting(value: unknown, key: string): string {
  try {
    return normalizeUsageStatsTimezone(value)
  } catch (error) {
    throw new Error(`${key} 无效：${error instanceof Error ? error.message : String(error)}`)
  }
}
