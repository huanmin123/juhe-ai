import { getBusinessDatabase, getStatsDatabase, nowIso } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { clearSharedJsonCacheInBackground, createAppCache, createSharedJsonCache } from '../shared/cache.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { errorLogFields, logger } from '../shared/logger.js'
import { clearUsageStatsTimezoneCache, normalizeUsageStatsTimezone, usageStatsTimezone } from './usage-stats-helpers.js'
import { getPostgresPool } from './postgres-client.js'
import { sqlPlaceholders } from './query-utils.js'
import { runtimeConfig } from '../config/runtime.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

interface GlobalSettingRow {
  key: string
  value_json: string
  updated_at: string
}

const SYSTEM_SETTINGS_ACCOUNT_ID = 'sys_admin'
const settingsCacheTtlMs = 60_000
const businessSchemaName = 'juhe_business'
export const systemSettingKeys = [
  'gatewayTextRawBodyLimitMegabytes',
  'accountCircuitConfirmationFailuresRequired',
  'gatewayUserRequestLimitPerMinute',
  'gatewayUserRequestLimitPerDay',
  'gatewayUserRequestLimitPerWeek',
  'gatewayUserRequestLimitPerMonth',
  'systemApiRateLimitIpReadPerMinute',
  'systemApiRateLimitIpReadBurstPer10Seconds',
  'systemApiRateLimitIpWritePerMinute',
  'systemApiRateLimitIpWriteBurstPer10Seconds',
  'systemApiRateLimitUserReadPerMinute',
  'systemApiRateLimitUserWritePerMinute',
  'defaultTemporaryUnschedulableMinutes',
  'temporaryUnschedulableRetryIntervalSeconds',
  'temporaryUnschedulableRetryAttempts',
  'textFirstResponseTimeoutSeconds',
  'textStreamIdleTimeoutSeconds',
  'textUncommittedAttemptMaxLifetimeSeconds',
  'imageFirstResponseTimeoutSeconds',
  'imageStreamIdleTimeoutSeconds',
  'imageUncommittedAttemptMaxLifetimeSeconds',
  'imageRequestWallTimeoutSeconds',
  'chatImageGenerationTotalTimeoutSeconds',
  'noAvailableAccountWaitTimeoutSeconds',
  'streamFailureThresholdCount',
  'streamFailureThresholdWindowMinutes',
  'operationLogRetentionDays',
  'operationLogMaxChangesPerRecord',
  'statsAggregationIntervalSeconds',
  'statsAggregationBatchSize',
  'statsAggregationMaxBatchesPerRun',
  'usageHotWindowRefreshIntervalSeconds',
  'groupAccountStatsRefreshIntervalSeconds',
  'systemMetricsSampleIntervalSeconds',
  'tableMonitorMaxTablesPerRun',
  'accountQualityRefreshIntervalSeconds',
  'accountQualityWindowMinutes',
  'accountTestTaskConcurrency',
  'accountHealthCheckIntervalHours',
  'accountHealthCheckJitterMinutes',
  'accountHealthCheckBatchSize',
  'accountHealthCheckFailureThreshold',
  'cooldownAccountRetestIntervalSeconds',
  'cooldownAccountRetestBatchSize',
  'cooldownAccountRetestMaxBackoffHours',
  'oauthAccessTokenRefreshIntervalSeconds',
  'oauthAccessTokenRefreshLeadSeconds',
  'oauthAccessTokenRefreshBatchSize',
  'oauthAccessTokenRefreshRetryBackoffSeconds',
  'modelCheckRetentionDays',
  'runtimeLogIndexRetentionDays',
  'publicApiLogRetentionDays',
  'usageRecordRetentionDays',
  'usageStatsTimezone',
  'usageStatsMinuteRetentionHours',
  'usageStatsHourlyRetentionDays',
  'usageStatsDailyRetentionDays',
  'usageStatsWeeklyRetentionWeeks',
  'usageStatsMonthlyRetentionMonths',
  'usageRankSnapshotRetentionDays',
  'systemMetricsRetentionDays',
  'systemMetricsHourlyRetentionDays'
] as const

const SYSTEM_SETTING_KEYS = new Set<string>(systemSettingKeys)
type SystemSettingKey = typeof systemSettingKeys[number]
type GlobalSettingKey = 'appName' | 'appIcon'
type SettingValidator = (value: unknown, key: string) => unknown
const compatibleSystemSettingDefaults: Partial<Record<SystemSettingKey, unknown>> = {
  gatewayUserRequestLimitPerMinute: 0,
  gatewayUserRequestLimitPerDay: 0,
  gatewayUserRequestLimitPerWeek: 0,
  gatewayUserRequestLimitPerMonth: 0
}

const globalSettingKeys = ['appName', 'appIcon'] as const
const GLOBAL_SETTING_KEYS = new Set<string>(globalSettingKeys)
export const managementSettingsSectionCatalog = {
  brand: { domain: 'global', keys: globalSettingKeys },
  'gateway-core': { domain: 'system', keys: ['gatewayTextRawBodyLimitMegabytes', 'accountCircuitConfirmationFailuresRequired', 'defaultTemporaryUnschedulableMinutes', 'temporaryUnschedulableRetryIntervalSeconds', 'temporaryUnschedulableRetryAttempts', 'textFirstResponseTimeoutSeconds', 'textStreamIdleTimeoutSeconds', 'textUncommittedAttemptMaxLifetimeSeconds', 'imageFirstResponseTimeoutSeconds', 'imageStreamIdleTimeoutSeconds', 'imageUncommittedAttemptMaxLifetimeSeconds', 'imageRequestWallTimeoutSeconds', 'chatImageGenerationTotalTimeoutSeconds', 'noAvailableAccountWaitTimeoutSeconds'] as const },
  'user-request-limit': { domain: 'system', keys: ['gatewayUserRequestLimitPerMinute', 'gatewayUserRequestLimitPerDay', 'gatewayUserRequestLimitPerWeek', 'gatewayUserRequestLimitPerMonth'] as const },
  'account-health': { domain: 'system', keys: ['accountHealthCheckIntervalHours', 'accountHealthCheckJitterMinutes', 'accountHealthCheckBatchSize', 'accountHealthCheckFailureThreshold'] as const },
  'api-rate-limit': { domain: 'system', keys: ['systemApiRateLimitIpReadPerMinute', 'systemApiRateLimitIpReadBurstPer10Seconds', 'systemApiRateLimitIpWritePerMinute', 'systemApiRateLimitIpWriteBurstPer10Seconds', 'systemApiRateLimitUserReadPerMinute', 'systemApiRateLimitUserWritePerMinute'] as const },
  'account-test': { domain: 'system', keys: ['accountTestTaskConcurrency'] as const },
  'cooldown-retest': { domain: 'system', keys: ['cooldownAccountRetestMaxBackoffHours'] as const },
  'data-retention': { domain: 'system', keys: ['usageRecordRetentionDays', 'runtimeLogIndexRetentionDays', 'publicApiLogRetentionDays'] as const }
} as const
export type ManagementSettingsSectionKey = keyof typeof managementSettingsSectionCatalog
const SYSTEM_SETTING_VALIDATORS: Record<SystemSettingKey, SettingValidator> = {
  gatewayTextRawBodyLimitMegabytes: integerSetting(1, 64),
  accountCircuitConfirmationFailuresRequired: integerSetting(1, 5),
  gatewayUserRequestLimitPerMinute: integerSetting(0, 1_000_000_000),
  gatewayUserRequestLimitPerDay: integerSetting(0, 1_000_000_000),
  gatewayUserRequestLimitPerWeek: integerSetting(0, 1_000_000_000),
  gatewayUserRequestLimitPerMonth: integerSetting(0, 1_000_000_000),
  systemApiRateLimitIpReadPerMinute: integerSetting(0, 1_000_000),
  systemApiRateLimitIpReadBurstPer10Seconds: integerSetting(0, 1_000_000),
  systemApiRateLimitIpWritePerMinute: integerSetting(0, 1_000_000),
  systemApiRateLimitIpWriteBurstPer10Seconds: integerSetting(0, 1_000_000),
  systemApiRateLimitUserReadPerMinute: integerSetting(0, 1_000_000),
  systemApiRateLimitUserWritePerMinute: integerSetting(0, 1_000_000),
  defaultTemporaryUnschedulableMinutes: integerSetting(1, 1440),
  temporaryUnschedulableRetryIntervalSeconds: integerSetting(0, 3600),
  temporaryUnschedulableRetryAttempts: integerSetting(0, 10),
  textFirstResponseTimeoutSeconds: integerSetting(10, 3600),
  textStreamIdleTimeoutSeconds: integerSetting(1, 3600),
  textUncommittedAttemptMaxLifetimeSeconds: integerSetting(60, 86400),
  imageFirstResponseTimeoutSeconds: integerSetting(10, 3600),
  imageStreamIdleTimeoutSeconds: integerSetting(1, 3600),
  imageUncommittedAttemptMaxLifetimeSeconds: integerSetting(60, 86400),
  imageRequestWallTimeoutSeconds: integerSetting(60, 86400),
  chatImageGenerationTotalTimeoutSeconds: integerSetting(60, 86400),
  noAvailableAccountWaitTimeoutSeconds: integerSetting(10, 3600),
  streamFailureThresholdCount: integerSetting(1, 100),
  streamFailureThresholdWindowMinutes: integerSetting(1, 1440),
  operationLogRetentionDays: integerSetting(1, 3650),
  operationLogMaxChangesPerRecord: integerSetting(1, 500),
  statsAggregationIntervalSeconds: integerSetting(5, 3600),
  statsAggregationBatchSize: integerSetting(100, 10000),
  statsAggregationMaxBatchesPerRun: integerSetting(1, 100),
  usageHotWindowRefreshIntervalSeconds: integerSetting(60, 3600),
  groupAccountStatsRefreshIntervalSeconds: integerSetting(5, 3600),
  systemMetricsSampleIntervalSeconds: integerSetting(5, 3600),
  tableMonitorMaxTablesPerRun: integerSetting(0, 100),
  accountQualityRefreshIntervalSeconds: integerSetting(60, 3600),
  accountQualityWindowMinutes: integerSetting(1, 60),
  accountTestTaskConcurrency: integerSetting(1, 1000),
  accountHealthCheckIntervalHours: integerSetting(1, 168),
  accountHealthCheckJitterMinutes: integerSetting(0, 1440),
  accountHealthCheckBatchSize: integerSetting(1, 100),
  accountHealthCheckFailureThreshold: integerSetting(1, 10),
  cooldownAccountRetestIntervalSeconds: integerSetting(1, 3600),
  cooldownAccountRetestBatchSize: integerSetting(1, 100),
  cooldownAccountRetestMaxBackoffHours: integerSetting(1, 720),
  oauthAccessTokenRefreshIntervalSeconds: integerSetting(10, 3600),
  oauthAccessTokenRefreshLeadSeconds: integerSetting(60, 86400),
  oauthAccessTokenRefreshBatchSize: integerSetting(1, 200),
  oauthAccessTokenRefreshRetryBackoffSeconds: integerSetting(0, 86400),
  modelCheckRetentionDays: integerSetting(1, 365),
  runtimeLogIndexRetentionDays: integerSetting(1, 90),
  publicApiLogRetentionDays: integerSetting(1, 365),
  usageRecordRetentionDays: integerSetting(1, 180),
  usageStatsTimezone: timezoneSetting,
  usageStatsMinuteRetentionHours: integerSetting(1, 24 * 14),
  usageStatsHourlyRetentionDays: integerSetting(1, 180),
  usageStatsDailyRetentionDays: integerSetting(1, 800),
  usageStatsWeeklyRetentionWeeks: integerSetting(1, 260),
  usageStatsMonthlyRetentionMonths: integerSetting(1, 60),
  usageRankSnapshotRetentionDays: integerSetting(1, 365),
  systemMetricsRetentionDays: integerSetting(1, 7),
  systemMetricsHourlyRetentionDays: integerSetting(1, 30)
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
const systemSettingsSharedCache = createSharedJsonCache<Record<string, unknown>>({
  name: 'settings:system',
  max: 1,
  ttlMs: settingsCacheTtlMs
})
const globalSettingsSharedCache = createSharedJsonCache<Record<string, unknown>>({
  name: 'settings:global',
  max: 1,
  ttlMs: settingsCacheTtlMs
})

export function listGlobalSettings(): Record<string, unknown> {
  assertSyncSettingsReadAllowed('listGlobalSettings')
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

export function listGlobalSettingsReadOnly(): Record<string, unknown> {
  const rows = getBusinessDatabase().prepare("SELECT key, value_json, updated_at FROM global_settings WHERE key IN ('appName', 'appIcon') ORDER BY key ASC").all() as unknown as Array<GlobalSettingRow>
  const settings: Record<string, unknown> = {}
  for (const row of rows) {
    settings[row.key] = normalizeGlobalSetting(row.key, JSON.parse(row.value_json) as unknown)
  }
  assertAllSettingsPresent(settings, globalSettingKeys, '全局设置')
  return { ...settings }
}

export async function listGlobalSettingsAsync(): Promise<Record<string, unknown>> {
  if (runtimeConfig.cacheDriver !== 'redis') {
    const cached = globalSettingsCache.get('current')
    if (cached) {
      return { ...cached }
    }
  }
  const sharedCached = await getGlobalSettingsSharedCache()
  if (sharedCached) {
    globalSettingsCache.set('current', sharedCached)
    return { ...sharedCached }
  }
  const settings = sqliteReadWorkerPoolEnabled()
    ? await requestSqliteReadWorker({ type: 'list_global_settings_read_only' })
    : await loadGlobalSettingsFromDatabaseAsync()
  await setGlobalSettingsCacheAsync(settings)
  return { ...settings }
}

export function getManagementSettingsSectionReadOnly(sectionKey: ManagementSettingsSectionKey): Record<string, unknown> {
  const section = managementSettingsSectionCatalog[sectionKey]
  const table = section.domain === 'global' ? 'global_settings' : 'system_settings'
  const where = section.domain === 'global'
    ? `key IN (${sqlPlaceholders(section.keys.length)})`
    : `system_account_id = ? AND key IN (${sqlPlaceholders(section.keys.length)})`
  const params = section.domain === 'global' ? [...section.keys] : [SYSTEM_SETTINGS_ACCOUNT_ID, ...section.keys]
  const rows = getBusinessDatabase().prepare(`SELECT key, value_json FROM ${table} WHERE ${where} ORDER BY key ASC`).all(...params) as Array<{ key: string; value_json: string }>
  const values: Record<string, unknown> = {}
  for (const row of rows) {
    values[row.key] = section.domain === 'global'
      ? normalizeGlobalSetting(row.key, JSON.parse(row.value_json) as unknown)
      : normalizeSystemSetting(row.key, JSON.parse(row.value_json) as unknown)
  }
  if (section.domain === 'system') applyCompatibleSystemSettingDefaults(values, section.keys)
  assertAllSettingsPresent(values, section.keys, `${sectionKey} 设置`)
  return values
}

export async function getManagementSettingsSectionAsync(sectionKey: ManagementSettingsSectionKey): Promise<Record<string, unknown>> {
  if (sqliteReadWorkerPoolEnabled()) {
    return await requestSqliteReadWorker({ type: 'get_management_settings_section_read_only', sectionKey })
  }
  const section = managementSettingsSectionCatalog[sectionKey]
  const client = await getSettingsDatabaseClient()
  const placeholders = client.dialect.bindPlaceholders(section.keys.length)
  const table = settingsTable(client, section.domain === 'global' ? 'global_settings' : 'system_settings')
  const where = section.domain === 'global'
    ? `key IN (${placeholders})`
    : `system_account_id = ? AND key IN (${placeholders})`
  const params = section.domain === 'global' ? [...section.keys] : [SYSTEM_SETTINGS_ACCOUNT_ID, ...section.keys]
  const rows = await client.query<{ key: string; value_json: string }>(`SELECT key, value_json FROM ${table} WHERE ${where} ORDER BY key ASC`, params)
  const values: Record<string, unknown> = {}
  for (const row of rows) {
    values[row.key] = section.domain === 'global'
      ? normalizeGlobalSetting(row.key, JSON.parse(row.value_json) as unknown)
      : normalizeSystemSetting(row.key, JSON.parse(row.value_json) as unknown)
  }
  if (section.domain === 'system') applyCompatibleSystemSettingDefaults(values, section.keys)
  assertAllSettingsPresent(values, section.keys, `${sectionKey} 设置`)
  return values
}

export async function updateManagementSettingsSectionAsync(sectionKey: ManagementSettingsSectionKey, input: Record<string, unknown>): Promise<Record<string, unknown>> {
  const section = managementSettingsSectionCatalog[sectionKey]
  const allowed = new Set<string>(section.keys)
  const keys = Object.keys(input)
  if (keys.length === 0) throw new Error('设置更新不能为空')
  if (keys.some((key) => !allowed.has(key) || key === '__proto__' || key === 'constructor' || key === 'prototype')) {
    throw new Error(`${sectionKey} 包含不允许的字段`)
  }
  const normalized: Record<string, unknown> = {}
  for (const key of keys) {
    normalized[key] = section.domain === 'global'
      ? normalizeGlobalSetting(key, input[key])
      : normalizeSystemSetting(key, input[key])
  }
  if (section.domain === 'system') assertUsageStatsTimezoneUpdateAllowed(normalized)
  const client = await getSettingsDatabaseClient()
  const now = nowIso()
  await client.transaction(async (tx) => {
    for (const [key, value] of Object.entries(normalized)) {
      const table = settingsTable(tx, section.domain === 'global' ? 'global_settings' : 'system_settings')
      const columns = section.domain === 'global' ? '(key, value_json, updated_at)' : '(system_account_id, key, value_json, updated_at)'
      const values = section.domain === 'global' ? [key, JSON.stringify(value), now] : [SYSTEM_SETTINGS_ACCOUNT_ID, key, JSON.stringify(value), now]
      await tx.execute(`INSERT INTO ${table} ${columns} VALUES (${values.map(() => '?').join(', ')}) ON CONFLICT(${section.domain === 'global' ? 'key' : 'system_account_id, key'}) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, values)
    }
  })
  if (section.domain === 'global') await refreshGlobalSettingsCacheAfterSectionWrite()
  else {
    await refreshSystemSettingsCacheAfterSectionWrite()
    notifyGatewayRuntimeCacheInvalidation('settings_updated')
  }
  return getManagementSettingsSectionAsync(sectionKey)
}

async function refreshSystemSettingsCacheAfterSectionWrite(): Promise<void> {
  systemSettingsCache.clear()
  clearUsageStatsTimezoneCache()
  if (runtimeConfig.cacheDriver === 'redis') await systemSettingsSharedCache.clear()
  const settings = await loadSystemSettingsFromDatabaseAsync()
  await setSystemSettingsCacheAsync(settings)
}

async function refreshGlobalSettingsCacheAfterSectionWrite(): Promise<void> {
  globalSettingsCache.clear()
  if (runtimeConfig.cacheDriver === 'redis') await globalSettingsSharedCache.clear()
  const settings = await loadGlobalSettingsFromDatabaseAsync()
  await setGlobalSettingsCacheAsync(settings)
}

async function loadGlobalSettingsFromDatabaseAsync(): Promise<Record<string, unknown>> {
  const client = await getSettingsDatabaseClient()
  const rows = await client.query<GlobalSettingRow>(`
    SELECT key, value_json, updated_at
    FROM ${settingsTable(client, 'global_settings')}
    WHERE key IN ('appName', 'appIcon')
    ORDER BY key ASC
  `)
  const settings: Record<string, unknown> = {}
  for (const row of rows) {
    settings[row.key] = normalizeGlobalSetting(row.key, JSON.parse(row.value_json) as unknown)
  }
  assertAllSettingsPresent(settings, globalSettingKeys, '全局设置')
  return settings
}

export function listPublicGlobalSettings(): Record<string, unknown> {
  return pickGlobalSettings(listGlobalSettings())
}

export async function listPublicGlobalSettingsAsync(): Promise<Record<string, unknown>> {
  return pickGlobalSettings(await listGlobalSettingsAsync())
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

export async function updateGlobalSettingsAsync(input: Record<string, unknown>): Promise<Record<string, unknown>> {
  const normalizedInput = normalizeGlobalSettingsInput(input)
  const client = await getSettingsDatabaseClient()
  const now = nowIso()
  await client.transaction(async (tx) => {
    for (const [key, value] of Object.entries(normalizedInput)) {
      await tx.execute(`
        INSERT INTO ${settingsTable(tx, 'global_settings')} (key, value_json, updated_at)
        VALUES (?, ?, ?)
        ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at
      `, [key, JSON.stringify(value), now])
    }
  })
  clearGlobalSettingsCache()
  const settings = await loadGlobalSettingsFromDatabaseAsync()
  await setGlobalSettingsCacheAsync(settings)
  return { ...settings }
}

function pickGlobalSettings(input: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(input)
    .filter(([key]) => GLOBAL_SETTING_KEYS.has(key)))
}

export function getSettings(): Record<string, unknown> {
  assertSyncSettingsReadAllowed('getSettings')
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
  applyCompatibleSystemSettingDefaults(settings, systemSettingKeys)
  assertAllSettingsPresent(settings, systemSettingKeys, '系统设置')
  systemSettingsCache.set('current', settings)
  return { ...settings }
}

export function getSettingsReadOnly(): Record<string, unknown> {
  const systemAccountId = SYSTEM_SETTINGS_ACCOUNT_ID
  const rows = getBusinessDatabase()
    .prepare(`SELECT key, value_json FROM system_settings WHERE system_account_id = ? AND key IN (${sqlPlaceholders(systemSettingKeys.length)}) ORDER BY key ASC`)
    .all(systemAccountId, ...systemSettingKeys) as Array<{ key: string; value_json: string }>
  const settings: Record<string, unknown> = {}
  for (const row of rows) {
    settings[row.key] = normalizeSystemSetting(row.key, JSON.parse(row.value_json) as unknown)
  }
  applyCompatibleSystemSettingDefaults(settings, systemSettingKeys)
  assertAllSettingsPresent(settings, systemSettingKeys, '系统设置')
  return { ...settings }
}

export async function getSettingsAsync(): Promise<Record<string, unknown>> {
  if (runtimeConfig.cacheDriver !== 'redis') {
    const cached = systemSettingsCache.get('current')
    if (cached) {
      return { ...cached }
    }
  }
  const sharedCached = await getSystemSettingsSharedCache()
  if (sharedCached) {
    systemSettingsCache.set('current', sharedCached)
    return { ...sharedCached }
  }
  const settings = sqliteReadWorkerPoolEnabled()
    ? await requestSqliteReadWorker({ type: 'get_settings_read_only' })
    : await loadSystemSettingsFromDatabaseAsync()
  await setSystemSettingsCacheAsync(settings)
  return { ...settings }
}

async function loadSystemSettingsFromDatabaseAsync(): Promise<Record<string, unknown>> {
  const systemAccountId = SYSTEM_SETTINGS_ACCOUNT_ID
  const client = await getSettingsDatabaseClient()
  const rows = await client.query<{ key: string; value_json: string }>(`
    SELECT key, value_json
    FROM ${settingsTable(client, 'system_settings')}
    WHERE system_account_id = ? AND key IN (${client.dialect.bindPlaceholders(systemSettingKeys.length)})
    ORDER BY key ASC
  `, [systemAccountId, ...systemSettingKeys])
  const settings: Record<string, unknown> = {}
  for (const row of rows) {
    settings[row.key] = normalizeSystemSetting(row.key, JSON.parse(row.value_json) as unknown)
  }
  applyCompatibleSystemSettingDefaults(settings, systemSettingKeys)
  assertAllSettingsPresent(settings, systemSettingKeys, '系统设置')
  return settings
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

export async function updateSettingsAsync(input: Record<string, unknown>): Promise<Record<string, unknown>> {
  const systemAccountId = SYSTEM_SETTINGS_ACCOUNT_ID
  const normalizedInput = normalizeSystemSettingsInput(input)
  assertUsageStatsTimezoneUpdateAllowed(normalizedInput)
  const client = await getSettingsDatabaseClient()
  const now = nowIso()
  await client.transaction(async (tx) => {
    for (const [key, value] of Object.entries(normalizedInput)) {
      await tx.execute(`
        INSERT INTO ${settingsTable(tx, 'system_settings')} (system_account_id, key, value_json, updated_at)
        VALUES (?, ?, ?, ?)
        ON CONFLICT(system_account_id, key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at
      `, [systemAccountId, key, JSON.stringify(value), now])
    }
  })
  clearSystemSettingsCache()
  notifyGatewayRuntimeCacheInvalidation('settings_updated')
  const settings = await loadSystemSettingsFromDatabaseAsync()
  await setSystemSettingsCacheAsync(settings)
  return { ...settings }
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
  clearSystemSettingsSharedCache()
  clearUsageStatsTimezoneCache()
}

function clearGlobalSettingsCache(): void {
  globalSettingsCache.clear()
  clearGlobalSettingsSharedCache()
}

async function getSystemSettingsSharedCache(): Promise<Record<string, unknown> | undefined> {
  const settings = await getSettingsSharedCache(systemSettingsSharedCache)
  if (!settings) return undefined
  try {
    return normalizeSystemSettingsSnapshot(settings)
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'system_settings_shared_cache_schema_mismatch'
    }), '系统设置 Redis shared cache 与当前字段结构不一致，已忽略本次缓存')
    clearSystemSettingsSharedCache()
    return undefined
  }
}

async function getGlobalSettingsSharedCache(): Promise<Record<string, unknown> | undefined> {
  const settings = await getSettingsSharedCache(globalSettingsSharedCache)
  if (!settings) return undefined
  try {
    return normalizeGlobalSettingsSnapshot(settings)
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'global_settings_shared_cache_schema_mismatch'
    }), '全局设置 Redis shared cache 与当前字段结构不一致，已忽略本次缓存')
    clearGlobalSettingsSharedCache()
    return undefined
  }
}

async function getSettingsSharedCache(cache: typeof systemSettingsSharedCache): Promise<Record<string, unknown> | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return undefined
  const value = await cache.get('current')
  return value ? { ...value } : undefined
}

async function setSystemSettingsCacheAsync(settings: Record<string, unknown>): Promise<void> {
  await setSettingsSharedCache(systemSettingsSharedCache, settings)
  systemSettingsCache.set('current', settings)
}

async function setGlobalSettingsCacheAsync(settings: Record<string, unknown>): Promise<void> {
  await setSettingsSharedCache(globalSettingsSharedCache, settings)
  globalSettingsCache.set('current', settings)
}

async function setSettingsSharedCache(
  cache: typeof systemSettingsSharedCache,
  settings: Record<string, unknown>
): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  await cache.set('current', { ...settings }, { ttlMs: settingsCacheTtlMs })
}

function clearSystemSettingsSharedCache(): void {
  clearSettingsSharedCache(systemSettingsSharedCache)
}

function clearGlobalSettingsSharedCache(): void {
  clearSettingsSharedCache(globalSettingsSharedCache)
}

function clearSettingsSharedCache(cache: typeof systemSettingsSharedCache): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  clearSharedJsonCacheInBackground(
    cache,
    'settings_shared_cache_clear_failed',
    '系统设置 Redis shared cache 清理失败'
  )
}

function assertSyncSettingsReadAllowed(operation: string): void {
  if (runtimeConfig.databaseDriver === 'postgres' || runtimeConfig.runtimeMode === 'performance') {
    throw new Error(`高性能模式禁止同步读取本地 settings 缓存或 SQLite：${operation} 必须使用 async PG + Redis 路径`)
  }
}

async function getSettingsDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function settingsTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function assertUsageStatsTimezoneUpdateAllowed(input: Record<string, unknown>): void {
  if (!Object.prototype.hasOwnProperty.call(input, 'usageStatsTimezone')) {
    return
  }
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('PostgreSQL 模式下暂不支持在线修改统计时区，请停机后通过离线迁移 / 重建流程调整')
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

function normalizeSystemSettingsSnapshot(input: Record<string, unknown>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const key of systemSettingKeys) {
    const value = Object.prototype.hasOwnProperty.call(input, key)
      ? input[key]
      : compatibleSystemSettingDefaults[key]
    output[key] = normalizeSystemSetting(key, value)
  }
  assertAllSettingsPresent(output, systemSettingKeys, '系统设置')
  return output
}

function applyCompatibleSystemSettingDefaults(settings: Record<string, unknown>, keys: readonly string[]): void {
  for (const key of keys) {
    if (Object.prototype.hasOwnProperty.call(settings, key)) continue
    const fallback = compatibleSystemSettingDefaults[key as SystemSettingKey]
    if (fallback !== undefined) settings[key] = fallback
  }
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

function normalizeGlobalSettingsSnapshot(input: Record<string, unknown>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const key of globalSettingKeys) {
    output[key] = normalizeGlobalSetting(key, input[key])
  }
  assertAllSettingsPresent(output, globalSettingKeys, '全局设置')
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
