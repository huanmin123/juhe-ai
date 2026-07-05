import { strict as assert } from 'node:assert'
import { DatabaseSync } from 'node:sqlite'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
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
  assertSourceGuards()

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
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const primaryGroup = repositories.createGroup({
    name: '脏缓存主分组',
    providerCode: 'gpt',
    description: '用于验证授权分组摘要展示'
  }, ownerAccess)
  const granteeTargetGroup = repositories.createGroup({
    name: '脏缓存被授权人目标分组',
    providerCode: 'gpt'
  }, granteeAccess)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    groupId: primaryGroup.id,
    name: '脏缓存账户',
    type: 'api_key',
    credentials: { api_key: 'sk-group-account-stats-dirty-cache', base_url: 'https://api.openai.com/v1' },
    status: 'active'
  }, ownerAccess)

  assert.deepEqual(dirtyRows().map((row) => row.group_id), [primaryGroup.id], '创建账户后应只标记所属分组为脏')
  assert.equal(groupStatsRow(primaryGroup.id), undefined, '请求链路不应同步重建 group_account_stats')
  const ownerGroupBeforeStatsRefresh = repositories.listGroupsPage(ownerAccess, { page: 1, pageSize: 20 }).items.find((group) => group.id === primaryGroup.id)
  assert.equal(ownerGroupBeforeStatsRefresh?.accountStats.total, 0, '统计缓存缺失时分组列表不在请求链路实时回算账户总数')
  assert.equal(ownerGroupBeforeStatsRefresh?.accountStats.available, 0, '统计缓存缺失时分组列表不在请求链路实时回算可用账户数')
  assertBusinessIndexExists('idx_group_accounts_group_enabled')

  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, 'worker 应按脏分组刷新统计缓存')
  assert.deepEqual(dirtyRows(), [], '脏分组刷新完成后应清空对应队列')
  assert.equal(groupStatsRow(primaryGroup.id)?.total, 1, 'worker 刷新后应写入分组账户统计')
  const ownerGroupAfterStatsRefresh = repositories.listGroupsPage(ownerAccess, { page: 1, pageSize: 20 }).items.find((group) => group.id === primaryGroup.id)
  const ownerGroupAfterStatsRefreshAsync = (await repositories.listGroupsPageAsync(ownerAccess, { page: 1, pageSize: 20 })).items.find((group) => group.id === primaryGroup.id)
  assert.equal(ownerGroupAfterStatsRefresh?.accountStats.total, 1, '同步分组列表应读取预聚合账户总数')
  assert.equal(ownerGroupAfterStatsRefresh?.accountStats.available, 1, '同步分组列表应读取预聚合可用账户数')
  assert.equal(ownerGroupAfterStatsRefreshAsync?.accountStats.total, ownerGroupAfterStatsRefresh?.accountStats.total, '异步分组列表应读取同一预聚合账户总数')
  assert.equal(ownerGroupAfterStatsRefreshAsync?.accountStats.available, ownerGroupAfterStatsRefresh?.accountStats.available, '异步分组列表应读取同一预聚合可用账户数')
  assert.equal(ownerGroupAfterStatsRefreshAsync?.accountStats.active, ownerGroupAfterStatsRefresh?.accountStats.active, '异步分组列表应读取同一预聚合正常账户数')

  const statusLockGroup = repositories.createGroup({
    name: '脏缓存状态写入锁库分组',
    providerCode: 'gpt'
  }, ownerAccess)
  const statusLockAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    groupId: statusLockGroup.id,
    name: '锁库状态写入账户',
    type: 'api_key',
    credentials: { api_key: 'sk-group-account-stats-dirty-cache-status-lock', base_url: 'https://api.openai.com/v1' },
    status: 'active'
  }, ownerAccess)
  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, '状态写入锁库回归准备账户应先刷新一次统计缓存')
  withStatsWriteLock(() => {
    assert.doesNotThrow(
      () => repositories.markAccountCooldown(statusLockAccount.id, new Date(Date.now() + 60_000).toISOString(), '统计库锁定时账户冷却', 'rate_limited'),
      '统计结果库写锁占用时，账户状态写入不应因分组统计脏标记失败'
    )
    assert.equal(accountRow(statusLockAccount.id)?.status, 'rate_limited', '锁库期间账户状态仍应写入业务库')
    assert.deepEqual(dirtyRows(), [{ group_id: statusLockGroup.id, reason: 'account_cooldown' }], '锁库期间账户状态写入应只标记业务库脏队列')
  })
  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, '统计锁释放后 worker 应消费账户状态写入脏标记')
  assert.deepEqual(dirtyRows(), [], '账户状态写入脏标记刷新完成后应被清空')

  const lockGroup = repositories.createGroup({
    name: '脏缓存锁库回归分组',
    providerCode: 'gpt'
  }, ownerAccess)
  const lockExpiredAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    groupId: lockGroup.id,
    name: '锁库过期账户',
    type: 'api_key',
    credentials: { api_key: 'sk-group-account-stats-dirty-cache-lock-expired', base_url: 'https://api.openai.com/v1' },
    status: 'active'
  }, ownerAccess)
  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, '锁库回归准备账户应先刷新一次统计缓存')
  const expiredAt = new Date(Date.now() - 60_000).toISOString()
  databaseModule.getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'temporary_unavailable',
          schedulable = 1,
          cooldown_until = ?,
          account_expires_at = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(expiredAt, expiredAt, expiredAt, lockExpiredAccount.id)
  withStatsWriteLock(() => {
    assert.doesNotThrow(() => repositories.listAccountsDueForCooldownRetest(20), '统计结果库写锁占用时，冷却复测候选扫描不应因分组统计脏标记写入失败')
    assert.equal(accountRow(lockExpiredAccount.id)?.status, 'disabled', '锁库期间过期账户仍应被业务库清理为停用')
    assert.deepEqual(dirtyRows(), [{ group_id: '__all__', reason: 'account_expired' }], '锁库期间分组统计脏标记应落在业务库队列')
  })
  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, '统计锁释放后 worker 应消费业务库脏标记并刷新统计缓存')
  assert.deepEqual(dirtyRows(), [], '业务库脏标记刷新完成后应被清空')

  repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    groupId: primaryGroup.id,
    name: '脏缓存新增账户',
    type: 'api_key',
    credentials: { api_key: 'sk-group-account-stats-dirty-cache-2', base_url: 'https://api.openai.com/v1' },
    status: 'active'
  }, ownerAccess)
  assert.deepEqual(dirtyRows().map((row) => row.group_id), [primaryGroup.id], '已有统计行的分组新增账户后应标记为脏')
  assert.equal(groupStatsRow(primaryGroup.id)?.total, 1, 'worker 刷新前统计缓存仍保留上次聚合值')
  const ownerDirtyGroupBeforeStatsRefresh = repositories.listGroupsPage(ownerAccess, { page: 1, pageSize: 20 }).items.find((group) => group.id === primaryGroup.id)
  assert.equal(groupStatsRow(primaryGroup.id)?.total, 1, '已标脏分组的列表读取不应同步改写统计缓存')
  assert.equal(ownerDirtyGroupBeforeStatsRefresh?.accountStats.total, 1, '统计行已标脏时分组列表仍展示 worker 上次聚合结果')
  assert.equal(ownerDirtyGroupBeforeStatsRefresh?.accountStats.available, 1, '统计行已标脏时分组列表仍展示 worker 上次聚合可用数')
  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, 'worker 应刷新已标脏分组的统计行')
  assert.deepEqual(dirtyRows(), [], '统计行刷新后应清空脏标记')
  assert.equal(groupStatsRow(primaryGroup.id)?.total, 2, 'worker 刷新后统计行应更新为最新账户数')

  for (let index = 0; index < 25; index += 1) {
    repositories.createGroup({
      name: `脏缓存批量分组 ${String(index).padStart(2, '0')}`,
      providerCode: 'gpt'
    }, ownerAccess)
  }
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeTargetGroup.id,
    remark: '验证全量失效只写哨兵'
  }, ownerAccess)

  assert.deepEqual(dirtyRows(), [{ group_id: '__all__', reason: 'resource_authorization_created' }], '授权这类全量影响写路径只能写 1 条哨兵，不能按分组展开')
  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(10), 1, 'worker 应能按固定批次消费全量哨兵')
  const cursorDirtyRows = dirtyRows()
  assert.equal(cursorDirtyRows.length, 1, '全量哨兵未完成时应保留游标等待下一轮')
  assert.equal(cursorDirtyRows[0].group_id, '__all__')
  assert.match(cursorDirtyRows[0].reason ?? '', /^all_cursor:/, '全量哨兵应记录已处理分组游标')
  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, 'worker 下一轮应继续消费全量哨兵剩余分组')
  assert.deepEqual(dirtyRows(), [], '全量哨兵全部刷新后应被清理')

  const expireLockGroup = repositories.createGroup({
    name: '脏缓存授权过期锁库分组',
    providerCode: 'gpt'
  }, ownerAccess)
  const expireLockAuthorization = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: expireLockGroup.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    expiresAt: new Date(Date.now() + 60_000).toISOString()
  }, ownerAccess)
  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, '授权过期锁库回归准备授权应先刷新一次统计缓存')
  const authorizationExpiredAt = new Date(Date.now() - 60_000).toISOString()
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE resource_authorization_grants SET expires_at = ?, updated_at = ? WHERE id = ?')
    .run(authorizationExpiredAt, authorizationExpiredAt, expireLockAuthorization.id)
  withStatsWriteLock(() => {
    assert.equal(repositories.expireDueResourceAuthorizations(), 1, '统计结果库写锁占用时，授权过期扫描仍应完成业务库状态更新')
    assert.equal(grantStatus(expireLockAuthorization.id), 'expired', '锁库期间授权 grant 仍应过期')
    assert.deepEqual(dirtyRows(), [{ group_id: '__all__', reason: 'authorization_expired' }], '锁库期间授权过期应只标记业务库全量脏队列')
  })
  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, '统计锁释放后 worker 应消费授权过期脏标记')
  assert.deepEqual(dirtyRows(), [], '授权过期脏标记刷新完成后应被清空')

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
  const authorizedGroupBeforeStatsRefresh = repositories.listGroupsPage({ systemAccountId: grantee.id, role: 'user' as const }, { page: 1, pageSize: 20 }).items.find((group) => group.id === primaryGroup.id)
  assert.equal(groupStatsRow(primaryGroup.id), undefined, '授权分组列表不应在请求链路写回 group_account_stats')
  assert.equal(authorizedGroupBeforeStatsRefresh?.accessType, 'authorized', '被授权用户应能在缓存缺失时看到授权分组')
  assert.equal(authorizedGroupBeforeStatsRefresh?.accountStats.total, 0, '统计缓存缺失时授权分组列表不在请求链路实时回算账户总数')
  assert.equal(authorizedGroupBeforeStatsRefresh?.accountStats.available, 0, '统计缓存缺失时授权分组列表不在请求链路实时回算可用账户数')

  assert.equal(usageStatsRepository.refreshDirtyGroupAccountStatsCache(), 1, '分组授权后 worker 应刷新统计缓存')
  const authorizedGroup = repositories.listGroupsPage({ systemAccountId: grantee.id, role: 'user' as const }, { page: 1, pageSize: 20 }).items
    .find((group) => group.id === primaryGroup.id)
  assert.equal(authorizedGroup?.accessType, 'authorized', '被授权用户应能在分组列表看到授权分组')
  assert.equal(authorizedGroup?.accountStats.total, 2, '授权分组列表应展示原分组聚合账户总数')
  assert.equal(authorizedGroup?.accountStats.available, 2, '授权分组列表应展示原分组聚合可用账户数')
  assert.equal(authorizedGroup?.description, '用于验证授权分组摘要展示', '授权分组列表应展示原分组说明')
  assert.deepEqual(authorizedGroup?.accountIds, [], '授权分组列表不应暴露具体账户 ID')
  const authorizedGroupAsync = (await repositories.listGroupsPageAsync(granteeAccess, { page: 1, pageSize: 20 })).items
    .find((group) => group.id === primaryGroup.id)
  assert.equal(authorizedGroupAsync?.accessType, 'authorized', '异步分组列表应能看到授权分组')
  assert.equal(authorizedGroupAsync?.accountStats.total, authorizedGroup?.accountStats.total, '异步授权分组列表应读取同一预聚合账户总数')
  assert.equal(authorizedGroupAsync?.accountStats.available, authorizedGroup?.accountStats.available, '异步授权分组列表应读取同一预聚合可用账户数')
  assert.deepEqual(authorizedGroupAsync?.accountIds, [], '异步授权分组列表不应暴露具体账户 ID')

  console.log('分组账户统计脏缓存回归通过：请求路径只打业务库脏标记，全量影响只写哨兵，统计由 worker 异步刷新，统计库写锁期间业务写入不受影响，缓存缺失或已标脏时列表只读预聚合结果，异步分组摘要读取同一统计口径')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function dirtyRows(): DirtyRow[] {
  const rows = databaseModule.getBusinessDatabase()
    .prepare('SELECT group_id, reason FROM group_account_stats_dirty ORDER BY group_id')
    .all() as unknown as DirtyRow[]
  return rows.map((row) => ({ group_id: row.group_id, reason: row.reason }))
}

function assertSourceGuards(): void {
  const source = readFileSync(resolve('src/storage/group-account-stats-cache.repository.ts'), 'utf8')
  assert.doesNotMatch(source, /SELECT id, system_account_id FROM groups'\)\.all\(\)/, '分组统计刷新不应一次性加载全部分组')
  assert.match(source, /GROUP_ACCOUNT_STATS_DIRTY_ALL_CURSOR_PREFIX/, '全量分组统计刷新应使用哨兵游标')
  assert.match(source, /loadGroupAccountStatsGroupsPage/, '全量分组统计刷新应按固定页读取分组')
  assert.match(source, /ORDER BY id ASC\s+LIMIT \?/, '全量分组统计刷新读取分组必须有固定 LIMIT')
  const invalidationSource = readFileSync(resolve('src/storage/group-account-stats-write-invalidation.ts'), 'utf8')
  assert.doesNotMatch(invalidationSource, /if \(runtimeConfig\.databaseDriver === 'postgres'\) \{\s*return\s*\}/, 'PG 模式下分组统计标脏不能静默跳过')
  assert.match(invalidationSource, /markPostgresGroupAccountStatsDirtyInBackground\(input\)/, 'PG 模式下同步标脏入口应转入异步写脏队列')
  const accountWriteSource = readFileSync(resolve('src/storage/repositories.ts'), 'utf8')
  assert.match(accountWriteSource, /await refreshGroupAccountStatsAfterWriteAsync\(\{ groupIds: \[groupId\], reason: 'account_created' \}\)/, 'PG 账户创建后必须标记分组账户统计脏队列')
  assert.match(accountWriteSource, /await refreshGroupAccountStatsAfterWriteAsync\(\{ accountIds: \[id\], reason: 'account_updated' \}\)/, 'PG 账户更新后必须按账户反查标记分组账户统计脏队列')
  const bindingSource = readFileSync(resolve('src/storage/account-group-binding-write.repository.ts'), 'utf8')
  assert.match(bindingSource, /await refreshGroupAccountStatsAfterWriteAsync\(\{ groupIds: \[previousGroupId, groupId\], reason: 'group_account_binding' \}\)/, 'PG 账户改绑分组后必须标记新旧分组统计脏队列')
  const groupWriteSource = readFileSync(resolve('src/storage/group-write.repository.ts'), 'utf8')
  assert.match(groupWriteSource, /await refreshGroupAccountStatsAfterWriteAsync\(\{ groupIds: \[id\], reason: 'group_deleted' \}\)/, 'PG 分组删除后必须标记统计脏队列以清理旧缓存行')
}

function accountRow(accountId: string): { status: string } | undefined {
  return databaseModule.getBusinessDatabase()
    .prepare('SELECT status FROM accounts WHERE id = ?')
    .get(accountId) as unknown as { status: string } | undefined
}

function grantStatus(authorizationGrantId: string): string | undefined {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT status FROM resource_authorization_grants WHERE id = ?')
    .get(authorizationGrantId) as unknown as { status?: string } | undefined
  return row?.status
}

function groupStatsRow(groupId: string): { total: number } | undefined {
  return databaseModule.getStatsDatabase()
    .prepare('SELECT total FROM group_account_stats WHERE group_id = ?')
    .get(groupId) as unknown as { total: number } | undefined
}

function assertBusinessIndexExists(indexName: string): void {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `业务库应创建索引 ${indexName}`)
}

function withStatsWriteLock(action: () => void): void {
  const statsLock = new DatabaseSync(runtimeConfig.statsDatabasePath)
  statsLock.exec('PRAGMA busy_timeout = 1; BEGIN IMMEDIATE')
  try {
    action()
  } finally {
    statsLock.exec('ROLLBACK')
    statsLock.close()
  }
}
