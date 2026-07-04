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
  'systemApiRateLimitEnabled',
  'systemApiRateLimitIpReadPerMinute',
  'systemApiRateLimitIpReadBurstPer10Seconds',
  'systemApiRateLimitIpWritePerMinute',
  'systemApiRateLimitIpWriteBurstPer10Seconds',
  'systemApiRateLimitUserReadPerMinute',
  'systemApiRateLimitUserWritePerMinute',
  'defaultTemporaryUnschedulableMinutes',
  'temporaryUnschedulableRetryIntervalSeconds',
  'temporaryUnschedulableRetryAttempts',
  'streamCircuitBreakerEnabled',
  'streamRequestTimeoutSeconds',
  'streamIdleTimeoutSeconds',
  'streamClientTotalWaitTimeoutSeconds',
  'streamMaxLifetimeSeconds',
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
  'accountTestTaskConcurrency',
  'accountHealthCheckIntervalHours',
  'accountHealthCheckJitterMinutes',
  'accountHealthCheckBatchSize',
  'accountHealthCheckFailureThreshold',
  'cooldownAccountRetestIntervalSeconds',
  'cooldownAccountRetestBatchSize',
  'cooldownAccountRetestMaxBackoffHours',
  'cooldownAccountRetestLongTermIntervalHours',
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
  'systemMetricsHourlyRetentionDays',
  'dataRetentionCleanupIntervalMinutes',
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
  gatewayTextRawBodyLimitMegabytes: integerSetting(1, 64),
  systemApiRateLimitEnabled: booleanSetting,
  systemApiRateLimitIpReadPerMinute: integerSetting(0, 1_000_000),
  systemApiRateLimitIpReadBurstPer10Seconds: integerSetting(0, 1_000_000),
  systemApiRateLimitIpWritePerMinute: integerSetting(0, 1_000_000),
  systemApiRateLimitIpWriteBurstPer10Seconds: integerSetting(0, 1_000_000),
  systemApiRateLimitUserReadPerMinute: integerSetting(0, 1_000_000),
  systemApiRateLimitUserWritePerMinute: integerSetting(0, 1_000_000),
  defaultTemporaryUnschedulableMinutes: integerSetting(1, 1440),
  temporaryUnschedulableRetryIntervalSeconds: integerSetting(0, 3600),
  temporaryUnschedulableRetryAttempts: integerSetting(0, 10),
  streamCircuitBreakerEnabled: booleanSetting,
  streamRequestTimeoutSeconds: integerSetting(10, 3600),
  streamIdleTimeoutSeconds: integerSetting(1, 3600),
  streamClientTotalWaitTimeoutSeconds: integerSetting(10, 3600),
  streamMaxLifetimeSeconds: integerSetting(60, 86400),
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
  accountTestTaskConcurrency: integerSetting(1, 1000),
  accountHealthCheckIntervalHours: integerSetting(1, 168),
  accountHealthCheckJitterMinutes: integerSetting(0, 1440),
  accountHealthCheckBatchSize: integerSetting(1, 100),
  accountHealthCheckFailureThreshold: integerSetting(1, 10),
  cooldownAccountRetestIntervalSeconds: integerSetting(1, 3600),
  cooldownAccountRetestBatchSize: integerSetting(1, 100),
  cooldownAccountRetestMaxBackoffHours: integerSetting(1, 720),
  cooldownAccountRetestLongTermIntervalHours: integerSetting(1, 720),
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
  systemMetricsHourlyRetentionDays: integerSetting(1, 30),
  dataRetentionCleanupIntervalMinutes: integerSetting(5, 1440),
  dataRetentionCleanupBatchSize: integerSetting(100, 5_000),
  dataRetentionCleanupMaxBatchesPerRun: integerSetting(1, 100)
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
  return listGlobalSettings()
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
  assertAllSettingsPresent(settings, systemSettingKeys, '系统设置')
  systemSettingsCache.set('current', settings)
  return { ...settings }
}

export function getSettingsReadOnly(): Record<string, unknown> {
  return getSettings()
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
  return getSettingsSharedCache(systemSettingsSharedCache)
}

async function getGlobalSettingsSharedCache(): Promise<Record<string, unknown> | undefined> {
  return getSettingsSharedCache(globalSettingsSharedCache)
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
