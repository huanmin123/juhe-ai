import { getDatabase, nowIso } from './database.js'

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
  'usageStatsDailyRetentionDays',
  'usageStatsHourlyRetentionDays',
  'systemMetricsRetentionDays',
  'systemMetricsHourlyRetentionDays',
  'dataRetentionCleanupBatchSize',
  'dataRetentionCleanupMaxBatchesPerRun'
] as const

const SYSTEM_SETTING_KEYS = new Set<string>(systemSettingKeys)

export function listGlobalSettings(): Record<string, unknown> {
  const rows = getDatabase().prepare('SELECT key, value_json, updated_at FROM global_settings ORDER BY key ASC').all() as unknown as Array<GlobalSettingRow>
  return Object.fromEntries(rows.map((row) => [row.key, JSON.parse(row.value_json) as unknown]))
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
  return Object.fromEntries(Object.entries(input).filter(([key]) => allowedKeys.has(key)))
}

export function getSettings(): Record<string, unknown> {
  const systemAccountId = SYSTEM_SETTINGS_ACCOUNT_ID
  const rows = getDatabase().prepare('SELECT key, value_json FROM system_settings WHERE system_account_id = ? ORDER BY key ASC').all(systemAccountId) as Array<{ key: string; value_json: string }>
  return Object.fromEntries(rows.filter((row) => isSystemSettingKey(row.key)).map((row) => [row.key, JSON.parse(row.value_json) as unknown]))
}

export function updateSettings(input: Record<string, unknown>): Record<string, unknown> {
  const systemAccountId = SYSTEM_SETTINGS_ACCOUNT_ID
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
