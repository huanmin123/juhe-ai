import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-group-account-stats-dirty-cache-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'group-account-stats-dirty-cache-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  usageStatsRepository
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js')
])

interface DirtyRow {
  group_id: string
  reason: string | null
}

interface CapturedSqlCall {
  sql: string
  params: unknown[]
}

try {
  const owner = repositories.createSystemAccount({
    username: 'group_stats_dirty_owner',
    displayName: '分组统计脏缓存所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'group_stats_dirty_grantee',
    displayName: '分组统计脏缓存被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const primaryGroup = repositories.createGroup({
    name: '脏缓存主分组',
    providerCode: 'openai',
    description: '用于验证授权分组摘要展示'
  }, ownerAccess)
  const account = repositories.createAccount({
    providerCode: 'openai',
    groupId: primaryGroup.id,
    name: '脏缓存账户',
    type: 'api_key',
    credentials: { api_key: 'sk-group-account-stats-dirty-cache', base_url: 'https://api.openai.com/v1' },
    status: 'active'
  }, ownerAccess)

  assert.deepEqual(dirtyRows().map((row) => row.group_id), [primaryGroup.id], '创建账户后应只标记所属分组为脏')
  assert.equal(groupStatsRow(primaryGroup.id), undefined, '请求链路不应同步重建 group_account_stats')
  const ownerFallbackCapture = captureBusinessSql(
    (sql) => /\bFROM\s+groups\b/i.test(sql) && /\bLEFT\s+JOIN\s+group_accounts\b/i.test(sql),
    () => repositories.listGroupsPage(ownerAccess, { page: 1, pageSize: 20 }).items
  )
  const ownerGroupBeforeStatsRefresh = ownerFallbackCapture.result.find((group) => group.id === primaryGroup.id)
  assertFallbackSqlIsWindowed(ownerFallbackCapture.calls, 21)
  assert.equal(groupStatsRow(primaryGroup.id), undefined, '分组列表兜底展示不应写回 group_account_stats')
  assert.equal(ownerGroupBeforeStatsRefresh?.accountStats.total, 1, '统计缓存缺失时分组列表仍应兜底展示账户总数')
  assert.equal(ownerGroupBeforeStatsRefresh?.accountStats.available, 1, '统计缓存缺失时分组列表仍应兜底展示可用账户数')
  assertBusinessIndexExists('idx_group_accounts_group_enabled')

  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, 'worker 应按脏分组刷新统计缓存')
  assert.deepEqual(dirtyRows(), [], '脏分组刷新完成后应清空对应队列')
  assert.equal(groupStatsRow(primaryGroup.id)?.total, 1, 'worker 刷新后应写入分组账户统计')

  repositories.createAccount({
    providerCode: 'openai',
    groupId: primaryGroup.id,
    name: '脏缓存新增账户',
    type: 'api_key',
    credentials: { api_key: 'sk-group-account-stats-dirty-cache-2', base_url: 'https://api.openai.com/v1' },
    status: 'active'
  }, ownerAccess)
  assert.deepEqual(dirtyRows().map((row) => row.group_id), [primaryGroup.id], '已有统计行的分组新增账户后应标记为脏')
  assert.equal(groupStatsRow(primaryGroup.id)?.total, 1, 'worker 刷新前统计缓存仍保留旧值')
  const ownerDirtyFallbackCapture = captureBusinessSql(
    (sql) => /\bFROM\s+groups\b/i.test(sql) && /\bLEFT\s+JOIN\s+group_accounts\b/i.test(sql),
    () => repositories.listGroupsPage(ownerAccess, { page: 1, pageSize: 20 }).items
  )
  const ownerDirtyGroupBeforeStatsRefresh = ownerDirtyFallbackCapture.result.find((group) => group.id === primaryGroup.id)
  assertFallbackSqlIsWindowed(ownerDirtyFallbackCapture.calls, 21)
  assert.equal(groupStatsRow(primaryGroup.id)?.total, 1, '已标脏分组的列表兜底不应同步改写统计缓存')
  assert.equal(ownerDirtyGroupBeforeStatsRefresh?.accountStats.total, 2, '统计行已标脏时分组列表应按当前页兜底展示最新账户总数')
  assert.equal(ownerDirtyGroupBeforeStatsRefresh?.accountStats.available, 2, '统计行已标脏时分组列表应按当前页兜底展示最新可用账户数')
  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, 'worker 应刷新已标脏分组的旧统计行')
  assert.deepEqual(dirtyRows(), [], '旧统计行刷新后应清空脏标记')
  assert.equal(groupStatsRow(primaryGroup.id)?.total, 2, 'worker 刷新后旧统计行应更新为最新账户数')

  for (let index = 0; index < 25; index += 1) {
    repositories.createGroup({
      name: `脏缓存批量分组 ${String(index).padStart(2, '0')}`,
      providerCode: 'openai'
    }, ownerAccess)
  }
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: '验证全量失效只写哨兵'
  }, ownerAccess)

  assert.deepEqual(dirtyRows(), [{ group_id: '__all__', reason: 'resource_authorization_created' }], '授权这类全量影响写路径只能写 1 条哨兵，不能按分组展开')
  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, 'worker 应能消费全量哨兵刷新统计缓存')
  assert.deepEqual(dirtyRows(), [], '全量哨兵刷新后应被清理')

  repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: primaryGroup.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: '验证授权分组统计展示'
  }, ownerAccess)
  databaseModule.getStatsDatabase()
    .prepare('DELETE FROM group_account_stats WHERE group_id = ?')
    .run(primaryGroup.id)
  const authorizedFallbackCapture = captureBusinessSql(
    (sql) => /\bFROM\s+groups\b/i.test(sql) && /\bLEFT\s+JOIN\s+group_accounts\b/i.test(sql),
    () => repositories.listGroupsPage({ systemAccountId: grantee.id, role: 'user' as const }, { page: 1, pageSize: 20 }).items
  )
  const authorizedGroupBeforeStatsRefresh = authorizedFallbackCapture.result.find((group) => group.id === primaryGroup.id)
  assertFallbackSqlIsWindowed(authorizedFallbackCapture.calls, 21)
  assert.equal(groupStatsRow(primaryGroup.id), undefined, '授权分组兜底展示不应写回 group_account_stats')
  assert.equal(authorizedGroupBeforeStatsRefresh?.accessType, 'authorized', '被授权用户应能在缓存缺失时看到授权分组')
  assert.equal(authorizedGroupBeforeStatsRefresh?.accountStats.total, 2, '统计缓存缺失时授权分组列表仍应兜底展示账户总数')
  assert.equal(authorizedGroupBeforeStatsRefresh?.accountStats.available, 2, '统计缓存缺失时授权分组列表仍应兜底展示可用账户数')

  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, '分组授权后 worker 应刷新统计缓存')
  const authorizedGroup = repositories.listGroupsPage({ systemAccountId: grantee.id, role: 'user' as const }, { page: 1, pageSize: 20 }).items
    .find((group) => group.id === primaryGroup.id)
  assert.equal(authorizedGroup?.accessType, 'authorized', '被授权用户应能在分组列表看到授权分组')
  assert.equal(authorizedGroup?.accountStats.total, 2, '授权分组列表应展示原分组聚合账户总数')
  assert.equal(authorizedGroup?.accountStats.available, 2, '授权分组列表应展示原分组聚合可用账户数')
  assert.equal(authorizedGroup?.description, '用于验证授权分组摘要展示', '授权分组列表应展示原分组说明')
  assert.deepEqual(authorizedGroup?.accountIds, [], '授权分组列表不应暴露具体账户 ID')

  console.log('分组账户统计脏缓存回归通过：请求路径只打脏标记，全量影响只写哨兵，统计由 worker 异步刷新，缓存缺失或已标脏时列表兜底展示账户数')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function dirtyRows(): DirtyRow[] {
  const rows = databaseModule.getStatsDatabase()
    .prepare('SELECT group_id, reason FROM group_account_stats_dirty ORDER BY group_id')
    .all() as unknown as DirtyRow[]
  return rows.map((row) => ({ group_id: row.group_id, reason: row.reason }))
}

function groupStatsRow(groupId: string): { total: number } | undefined {
  return databaseModule.getStatsDatabase()
    .prepare('SELECT total FROM group_account_stats WHERE group_id = ?')
    .get(groupId) as unknown as { total: number } | undefined
}

function captureBusinessSql<T>(predicate: (sql: string) => boolean, action: () => T): { result: T; calls: CapturedSqlCall[] } {
  const database = databaseModule.getDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const calls: CapturedSqlCall[] = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (predicate(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        calls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare
  try {
    return { result: action(), calls }
  } finally {
    database.prepare = originalPrepare
  }
}

function assertFallbackSqlIsWindowed(calls: CapturedSqlCall[], maxGroupParams: number): void {
  assert(calls.length > 0, '回归应捕获分组账户数兜底 SQL')
  for (const call of calls) {
    assert(/\bgroups\.id\s+IN\s*\(/i.test(call.sql), '兜底查询必须按当前页分组 ID 窗口读取')
    assert(call.params.length <= maxGroupParams, `兜底查询参数数量应受当前页窗口限制，实际 ${call.params.length}`)
    assert(!/\bINSERT\s+INTO\s+group_account_stats\b/i.test(call.sql), '兜底查询不能写入统计缓存')
    assert(!/\bDELETE\s+FROM\s+group_account_stats\b/i.test(call.sql), '兜底查询不能删除统计缓存')
    const planRows = databaseModule.getDatabase()
      .prepare(`EXPLAIN QUERY PLAN ${call.sql}`)
      .all(...call.params as SQLInputValue[]) as unknown as Array<{ detail?: string }>
    const details = planRows.map((row) => row.detail ?? '').join('\n')
    assert(!/SCAN\s+group_accounts\b/i.test(details), `兜底查询不应扫描 group_accounts：${details}`)
    assert(/SEARCH\s+group_accounts\b/i.test(details), `兜底查询应按 group_accounts 索引查找：${details}`)
  }
}

function assertBusinessIndexExists(indexName: string): void {
  const row = databaseModule.getDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `业务库应创建索引 ${indexName}`)
}
