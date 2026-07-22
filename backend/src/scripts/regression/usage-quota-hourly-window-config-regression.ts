import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-quota-hourly-window-config-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'quota-hourly-window-config.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'usage-quota-hourly-window-config-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  usageStatsRepository,
  quotaHourlyWindowsRepository
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/request-quota-hourly-windows.repository.js')
])

try {
  assertSourceGuards()

  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({ name: '额度小时窗口配置分组', providerCode: 'gpt' }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '额度小时窗口配置 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    quotaLimits: { hourly: { enabled: true, hours: 37, limit: 1 } }
  }, access)

  const businessDatabase = databaseModule.getBusinessDatabase()
  const now = new Date().toISOString()
  businessDatabase
    .prepare(`
      INSERT INTO system_accounts (
        id, username, display_name, role, status, password_hash, must_change_password, image_generation_enabled, created_at, updated_at
      ) VALUES (?, ?, ?, 'user', 'active', ?, 0, 0, ?, ?)
    `)
    .run('sys_quota_window_grantee', 'quota-window-grantee', '额度窗口被授权用户', 'test-password-hash', now, now)

  repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: group.id,
    granteeType: 'system_account',
    granteeId: 'sys_quota_window_grantee',
    limits: { hourly: { enabled: true, hours: 41, limit: 1 } }
  }, access)

  const configuredWindows = quotaHourlyWindowsRepository.listRequestQuotaHourlyWindowHours()
  assert(configuredWindows.includes(37), 'API Key 额度写入时应登记自定义小时窗口')
  assert(configuredWindows.includes(41), '统一授权额度写入时应登记自定义小时窗口')
  assert(configuredWindows.includes(720), '默认小时窗口仍应保留')
  assert(configuredWindows.length <= 720, '小时窗口配置读取必须被 1..720 的合法范围上限约束')

  const statsDatabase = databaseModule.getStatsDatabase()
  statsDatabase
    .prepare(`
      INSERT INTO usage_stats_hourly (
        system_account_id, scope_type, scope_id, stat_hour, total_cost_usd, updated_at
      ) VALUES (?, 'api_key', ?, ?, 0.37, ?)
    `)
    .run('sys_admin', apiKey.id, currentHourKey(), now)

  const originalPrepare = businessDatabase.prepare.bind(businessDatabase) as typeof businessDatabase.prepare
  let configQueryCount = 0
  businessDatabase.prepare = ((sql: string) => {
    const normalized = sql.replace(/\s+/g, ' ')
    if (/FROM api_keys\b.*quota_limits_json/i.test(normalized)
      || /FROM resource_authorizations\b.*limits_json/i.test(normalized)
      || /FROM resource_authorization_grants\b.*limits_json/i.test(normalized)) {
      throw new Error(`额度小时窗口刷新不应扫描额度 JSON 源表: ${normalized}`)
    }
    if (/FROM request_quota_hourly_window_configs\b/i.test(normalized)) {
      configQueryCount += 1
    }
    return originalPrepare(sql)
  }) as typeof businessDatabase.prepare

  try {
    usageStatsRepository.refreshUsageQuotaHourlyWindowsCache()
  } finally {
    businessDatabase.prepare = originalPrepare
  }

  assert.equal(configQueryCount, 1, '额度小时窗口刷新应只读取小型窗口配置表')
  const windowRow = statsDatabase
    .prepare(`
      SELECT total_cost_usd
      FROM usage_quota_hourly_windows
      WHERE system_account_id = 'sys_admin'
        AND scope_type = 'api_key'
        AND scope_id = ?
        AND window_hours = 37
    `)
    .get(apiKey.id) as { total_cost_usd?: number } | undefined
  assert.equal(windowRow?.total_cost_usd, 0.37, '自定义小时窗口应参与额度窗口刷新')

  console.log('额度小时窗口配置回归通过：worker 刷新只读取固定上限窗口配置，不扫描全部额度 JSON')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertSourceGuards(): void {
  const snapshotSource = readSource('storage/usage-stats-snapshot-helpers.ts')
  assert.match(snapshotSource, /listRequestQuotaHourlyWindowHours/, '额度小时窗口刷新必须读取配置表 helper')
  assert.doesNotMatch(snapshotSource, /SELECT\s+quota_limits_json\s+AS\s+limits_json\s+FROM\s+api_keys/i, '额度小时窗口刷新不应再扫描 api_keys.quota_limits_json')
  assert.doesNotMatch(snapshotSource, /SELECT\s+limits_json\s+FROM\s+resource_authorizations/i, '额度小时窗口刷新不应再扫描 resource_authorizations.limits_json')
  assert.doesNotMatch(snapshotSource, /SELECT\s+limits_json\s+FROM\s+resource_authorization_grants/i, '额度小时窗口刷新不应再扫描 resource_authorization_grants.limits_json')

  const configSource = readSource('storage/request-quota-hourly-windows.repository.ts')
  assert.match(configSource, /FROM request_quota_hourly_window_configs/, '额度小时窗口配置 helper 应读取专用小表')
  assert.match(configSource, /LIMIT \?/, '额度小时窗口配置读取必须有运行时 LIMIT')
  assert.match(configSource, /maxRequestQuotaHourlyWindowHours/, '额度小时窗口配置读取必须绑定合法小时上限')

  const apiKeySource = readSource('storage/api-key.repository.ts')
  assert.match(apiKeySource, /rememberRequestQuotaHourlyWindowsFromJson\(quotaLimitsJson, database, now\)/, 'API Key 写额度时必须登记小时窗口')

  const authorizationWriteSource = readSource('storage/resource-authorization-write-state.repository.ts')
  assert.match(authorizationWriteSource, /rememberRequestQuotaHourlyWindowsFromJson\(nextLimitsJson, input\.database, input\.now\)/, '授权写额度时必须登记小时窗口')
  assert.match(authorizationWriteSource, /rememberRequestQuotaHourlyWindowsFromJson\(grant\.limits_json, database, now\)/, '授权状态同步时必须补登记小时窗口')
}

function readSource(relativePath: string): string {
  return readFileSync(resolve('src', relativePath), 'utf8')
}

function currentHourKey(): string {
  return new Date().toISOString().slice(0, 13)
}
