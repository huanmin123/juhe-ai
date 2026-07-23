import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-management-list-contract-${Date.now()}-${Math.random().toString(16).slice(2)}`)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'management-list-contract-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, usageStatsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: '管理列表契约分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '管理列表契约账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-management-list-contract-account',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    status: 'active'
  }, access)
  assert.equal(account.status, 'pending_test', '管理列表 fixture 新建账户应先进入待检查状态')
  assert.equal(repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), true, '管理列表 fixture 应显式通过后台健康成功激活账户')
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '管理列表契约 API Key',
    groupBindings: [{ groupId: group.id, priority: 1, weight: 1, status: 'active' }]
  }, access)

  seedUsageStats([
    { scopeType: 'account', scopeId: account.id, requestCount: 7, inputTokens: 1200, outputTokens: 800, totalCost: 0.0123 },
    { scopeType: 'api_key', scopeId: apiKey.id, requestCount: 11, inputTokens: 2200, outputTokens: 900, totalCost: 0.0456 },
    { scopeType: 'group', scopeId: group.id, requestCount: 13, inputTokens: 3200, outputTokens: 1000, totalCost: 0.0789 }
  ])
  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, '回归准备应刷新分组账号统计预聚合')

  const accountList = repositories.listAccountsPage(access, { page: 1, pageSize: 20 })
  const listedAccount = accountList.items.find((item) => item.id === account.id)
  assert(listedAccount, 'AI 账户列表应返回种子账户')
  assert.equal(listedAccount.usage.requestCount, 7, 'AI 账户列表 usage 应读取预聚合真实总请求数')
  assert.equal(listedAccount.usage.inputTokens, 1200, 'AI 账户列表 usage 应读取预聚合真实输入 token')
  assert.equal(listedAccount.usage.outputTokens, 800, 'AI 账户列表 usage 应读取预聚合真实输出 token')
  assert.equal(typeof listedAccount.currentConcurrency, 'number', 'AI 账户列表 currentConcurrency 字段应保持数字')
  assert.equal(listedAccount.availabilityPresentation?.status, 'available', 'AI 账户列表必须返回统一用户状态 presentation')
  assertUsageContract(listedAccount.usage, 'AI 账户列表 usage')
  assertUsageContract(listedAccount.todayUsage, 'AI 账户列表 todayUsage')

  const apiKeyList = repositories.listApiKeysPage(access, { page: 1, pageSize: 20 })
  const listedApiKey = apiKeyList.items.find((item) => item.id === apiKey.id)
  assert(listedApiKey, 'API Key 列表应返回种子 Key')
  assert.equal(Object.prototype.hasOwnProperty.call(listedApiKey, 'key'), false, 'API Key 列表不应返回完整密钥字段')
  assert.equal(listedApiKey.routeStrategyId, apiKey.routeStrategyId, 'API Key 列表应保留 routeStrategyId')
  assert.equal(listedApiKey.status, 'active', 'API Key 列表应保留状态字段')
  assert.equal(Object.prototype.hasOwnProperty.call(listedApiKey, 'usage'), false, 'API Key 列表不应同步返回累计用量')
  const apiKeyUsage = await repositories.getApiKeyUsageByIdsAsync([apiKey.id], access)
  assert.equal(apiKeyUsage.items[0]?.usage.requestCount, 11, 'API Key 用量接口应读取预聚合真实总请求数')
  assert.equal(apiKeyUsage.items[0]?.usage.inputTokens, 2200, 'API Key 用量接口应读取预聚合真实输入 token')
  assert.equal(apiKeyUsage.items[0]?.usage.outputTokens, 900, 'API Key 用量接口应读取预聚合真实输出 token')
  assertUsageContract(apiKeyUsage.items[0]!.usage, 'API Key 用量接口 usage')

  const routeStrategyList = repositories.listRouteStrategyListItemsPage(access, { page: 1, pageSize: 20 })
  const listedRouteStrategy = routeStrategyList.items.find((item) => item.id === apiKey.routeStrategyId)
  assert(listedRouteStrategy, '策略路由轻量列表应返回种子策略路由')
  assert.equal(Object.prototype.hasOwnProperty.call(listedRouteStrategy, 'groupBindings'), false, '策略路由轻量列表不应返回完整分组绑定')
  assert.equal(Object.prototype.hasOwnProperty.call(listedRouteStrategy, 'hybridRoutingConfig'), false, '策略路由轻量列表不应返回混合智能完整配置')
  assert.equal(Object.prototype.hasOwnProperty.call(listedRouteStrategy, 'bindingCount'), false, '策略路由基础列表不应同步返回绑定数量')
  assert.equal(Object.prototype.hasOwnProperty.call(listedRouteStrategy, 'apiKeyCount'), false, '策略路由基础列表不应同步返回 API Key 数量')
  assert.equal(Object.prototype.hasOwnProperty.call(listedRouteStrategy, 'groupBindingPreview'), false, '策略路由基础列表不应同步返回分组预览')
  const routeStrategySnapshot = repositories.listRouteStrategyListSnapshot(access, [listedRouteStrategy.id])
  assert.equal(routeStrategySnapshot.items[0]?.bindingCount, 1, '策略路由 list snapshot 应返回绑定数量')
  assert.equal(routeStrategySnapshot.items[0]?.apiKeyCount, 1, '策略路由 list snapshot 应返回 API Key 数量')
  assert.equal(routeStrategySnapshot.items[0]?.groupBindingPreview[0]?.groupId, group.id, '策略路由 list snapshot 应返回分组预览')

  const groupList = repositories.listGroupsPage(access, { page: 1, pageSize: 20 })
  const listedGroup = groupList.items.find((item) => item.id === group.id)
  assert(listedGroup, '分组列表应返回种子分组')
  assert.deepEqual(listedGroup.accountIds, [], '分组分页列表不应返回完整账号 ID，避免列表读放大')
  assert.equal(listedGroup.accountStats.total, 1, '分组列表 accountStats.total 应读取预聚合真实账号数')
  assert.equal(listedGroup.accountStats.available, 1, '分组列表 accountStats.available 应读取预聚合真实可用账号数')
  assert.equal(listedGroup.accountStats.usage.requestCount, 13, '分组列表 accountStats.usage 应读取预聚合真实总请求数')
  assert.equal(listedGroup.accountStats.usage.inputTokens, 3200, '分组列表 accountStats.usage 应读取预聚合真实输入 token')
  assert.equal(listedGroup.accountStats.usage.outputTokens, 1000, '分组列表 accountStats.usage 应读取预聚合真实输出 token')
  assertGroupStatsContract(listedGroup.accountStats)
  const groupDetail = repositories.findGroupSummary(group.id, access)
  assert.deepEqual(groupDetail?.accountIds, [account.id], '分组详情应保留真实绑定账号 ID')

  assertStatsBusyDoesNotBlock(
    () => repositories.listApiKeysPage(access, { page: 1, pageSize: 20 }),
    'API Key 基础列表不应访问统计库'
  )
  assertStatsBusyFails(
    '分组列表统计读取',
    () => repositories.listGroupsPage(access, { page: 1, pageSize: 20 }),
    '分组列表统计库忙锁时必须明确失败，不能返回空 accountStats/usage 伪装成功'
  )

  console.log('管理列表契约回归通过：AI 账户/API Key/分组列表保留字段结构并读取真实预聚合统计')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedUsageStats(rows: Array<{
  scopeType: 'account' | 'api_key' | 'group'
  scopeId: string
  requestCount: number
  inputTokens: number
  outputTokens: number
  totalCost: number
}>): void {
  const now = new Date().toISOString()
  const statement = databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO usage_stats_totals (
        system_account_id, scope_type, scope_id,
        request_count, input_tokens, output_tokens, total_cost_usd,
        last_used_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
  for (const row of rows) {
    statement.run('sys_admin', row.scopeType, row.scopeId, row.requestCount, row.inputTokens, row.outputTokens, row.totalCost, now, now)
  }
}

function assertUsageContract(value: object, label: string): void {
  const record = value as Record<string, unknown>
  for (const key of [
    'requestCount',
    'inputTokens',
    'outputTokens',
    'cacheReadTokens',
    'cacheReadCost',
    'cacheWriteTokens',
    'cacheWrite1hTokens',
    'cacheWriteCost',
    'thinkingTokens',
    'inputImageTokens',
    'outputImageTokens',
    'totalTokens',
    'totalCost'
  ]) {
    assert.equal(typeof record[key], 'number', `${label}.${key} 应保持数字字段`)
  }
}

function assertGroupStatsContract(value: object): void {
  const record = value as Record<string, unknown>
  for (const key of [
    'total',
    'available',
    'active',
    'disabled',
    'error',
    'rateLimited',
    'currentConcurrency',
    'concurrencyLimit'
  ]) {
    assert.equal(typeof record[key], 'number', `分组列表 accountStats.${key} 应保持数字字段`)
  }
  assertUsageContract(record.todayUsage as object, '分组列表 accountStats.todayUsage')
  assertUsageContract(record.usage as object, '分组列表 accountStats.usage')
}

function assertStatsBusyFails(label: string, action: () => unknown, message: string): void {
  const statsDatabase = databaseModule.getStatsDatabase()
  const originalPrepare = statsDatabase.prepare.bind(statsDatabase) as typeof statsDatabase.prepare
  statsDatabase.prepare = ((sql: string) => {
    if (isManagementListStatsLookupSql(sql)) {
      throw sqliteBusyError()
    }
    return originalPrepare(sql)
  }) as typeof statsDatabase.prepare
  try {
    assert.throws(
      action,
      (error: unknown) => error instanceof Error && /database is locked/.test(error.message),
      `${label} 应透出 SQLite busy 错误`
    )
  } finally {
    statsDatabase.prepare = originalPrepare
  }
  assert.ok(true, message)
}

function assertStatsBusyDoesNotBlock(action: () => unknown, message: string): void {
  const statsDatabase = databaseModule.getStatsDatabase()
  const originalPrepare = statsDatabase.prepare.bind(statsDatabase) as typeof statsDatabase.prepare
  statsDatabase.prepare = ((sql: string) => {
    if (isManagementListStatsLookupSql(sql)) throw sqliteBusyError()
    return originalPrepare(sql)
  }) as typeof statsDatabase.prepare
  try {
    assert.doesNotThrow(action, message)
  } finally {
    statsDatabase.prepare = originalPrepare
  }
}

function isManagementListStatsLookupSql(sql: string): boolean {
  return /\bFROM\s+(usage_stats_totals|usage_stats_daily|group_account_stats)\b/i.test(sql)
}

function sqliteBusyError(): Error {
  const error = new Error('database is locked') as Error & { errcode?: number; errstr?: string; code?: string }
  error.errcode = 5
  error.errstr = 'database is locked'
  error.code = 'SQLITE_BUSY'
  return error
}
