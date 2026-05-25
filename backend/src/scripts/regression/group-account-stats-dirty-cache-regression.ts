import { strict as assert } from 'node:assert'
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

  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, 'worker 应按脏分组刷新统计缓存')
  assert.deepEqual(dirtyRows(), [], '脏分组刷新完成后应清空对应队列')
  assert.equal(groupStatsRow(primaryGroup.id)?.total, 1, 'worker 刷新后应写入分组账户统计')

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
  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, '分组授权后 worker 应刷新统计缓存')
  const authorizedGroup = repositories.listGroups({ systemAccountId: grantee.id, role: 'user' as const })
    .find((group) => group.id === primaryGroup.id)
  assert.equal(authorizedGroup?.accessType, 'authorized', '被授权用户应能在分组列表看到授权分组')
  assert.equal(authorizedGroup?.accountStats.total, 1, '授权分组列表应展示原分组聚合账户总数')
  assert.equal(authorizedGroup?.accountStats.available, 1, '授权分组列表应展示原分组聚合可用账户数')
  assert.equal(authorizedGroup?.description, '用于验证授权分组摘要展示', '授权分组列表应展示原分组说明')
  assert.deepEqual(authorizedGroup?.accountIds, [], '授权分组列表不应暴露具体账户 ID')

  console.log('分组账户统计脏缓存回归通过：请求路径只打脏标记，全量影响只写哨兵，统计由 worker 异步刷新，授权分组列表展示聚合账户数')
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
