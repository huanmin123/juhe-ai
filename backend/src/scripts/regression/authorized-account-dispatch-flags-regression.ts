import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorized-account-dispatch-flags-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'authorized-account-dispatch-flags.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorized-account-dispatch-flags-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  const owner = repositories.createSystemAccount({
    username: 'dispatch_owner',
    displayName: '调度所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'dispatch_grantee',
    displayName: '调度被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const otherGrantee = repositories.createSystemAccount({
    username: 'dispatch_other_grantee',
    displayName: '另一个调度被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const otherGranteeAccess = { systemAccountId: otherGrantee.id, role: 'user' as const }

  const superAccount = repositories.createAccount({
    providerCode: 'openai',
    name: 'B 授权超级优先账户',
    type: 'api_key',
    credentials: { api_key: 'sk-authorized-super', base_url: 'https://api.openai.com/v1' },
    superPriorityEnabled: true,
    priority: 10
  }, ownerAccess)
  const normalAccount = repositories.createAccount({
    providerCode: 'openai',
    name: 'A 授权普通账户',
    type: 'api_key',
    credentials: { api_key: 'sk-authorized-normal', base_url: 'https://api.openai.com/v1' },
    priority: 0
  }, ownerAccess)
  const fallbackAccount = repositories.createAccount({
    providerCode: 'openai',
    name: 'C 授权降级备用账户',
    type: 'api_key',
    credentials: { api_key: 'sk-authorized-fallback', base_url: 'https://api.openai.com/v1' },
    fallbackEnabled: true,
    priority: 0
  }, ownerAccess)
  const granteeOwnedAccount = repositories.createAccount({
    providerCode: 'openai',
    name: 'D 被授权人自有账户',
    type: 'api_key',
    credentials: { api_key: 'sk-authorized-owned-target', base_url: 'https://api.openai.com/v1' },
    priority: 0
  }, granteeAccess)
  const granteeGroup = repositories.createGroup({
    name: '被授权人调度分组',
    providerCode: 'openai'
  }, granteeAccess)
  const otherGranteeGroup = repositories.createGroup({
    name: '另一个被授权人调度分组',
    providerCode: 'openai'
  }, otherGranteeAccess)

  for (const account of [superAccount, normalAccount, fallbackAccount]) {
    repositories.createResourceAuthorization({
      resourceType: 'account',
      resourceId: account.id,
      granteeType: 'system_account',
      granteeId: grantee.id,
      targetGroupId: granteeGroup.id,
      remark: '调度标记回归'
    }, ownerAccess)
    repositories.createResourceAuthorization({
      resourceType: 'account',
      resourceId: account.id,
      granteeType: 'system_account',
      granteeId: otherGrantee.id,
      targetGroupId: otherGranteeGroup.id,
      remark: '调度标记第二被授权人回归'
    }, ownerAccess)
  }
  const granteeSuperAccount = authorizedInstanceForSource(superAccount.id, granteeAccess)
  const granteeNormalAccount = authorizedInstanceForSource(normalAccount.id, granteeAccess)
  const granteeFallbackAccount = authorizedInstanceForSource(fallbackAccount.id, granteeAccess)
  const otherGranteeSuperAccount = authorizedInstanceForSource(superAccount.id, otherGranteeAccess)
  const otherGranteeNormalAccount = authorizedInstanceForSource(normalAccount.id, otherGranteeAccess)
  const otherGranteeFallbackAccount = authorizedInstanceForSource(fallbackAccount.id, otherGranteeAccess)

  for (const account of [granteeSuperAccount, granteeNormalAccount, granteeFallbackAccount, granteeOwnedAccount]) {
    const bound = repositories.setAccountGroup(account.id, granteeGroup.id, granteeAccess)
    assert(bound, `授权账户绑定分组失败：${account.name}`)
  }
  for (const account of [otherGranteeSuperAccount, otherGranteeNormalAccount, otherGranteeFallbackAccount]) {
    const bound = repositories.setAccountGroup(account.id, otherGranteeGroup.id, otherGranteeAccess)
    assert(bound, `第二被授权人绑定分组失败：${account.name}`)
  }

  const dispatchAccounts = repositories.listOpenAIAccountsForGroup(granteeGroup.id, grantee.id)
  const dispatchById = new Map(dispatchAccounts.map((account) => [account.id, account]))
  assert.equal(dispatchById.get(granteeSuperAccount.id)?.superPriorityEnabled, false, '授权实例不应继承所有者超级优先')
  assert.equal(dispatchById.get(granteeSuperAccount.id)?.priority, 0, '授权实例不应继承所有者优先级')
  assert.equal(dispatchById.get(granteeFallbackAccount.id)?.fallbackEnabled, false, '授权实例不应继承所有者降级备用')
  assert.deepEqual(
    dispatchIds(granteeGroup.id, grantee.id, [granteeNormalAccount.id, granteeSuperAccount.id, granteeFallbackAccount.id]),
    [granteeNormalAccount.id, granteeSuperAccount.id, granteeFallbackAccount.id],
    '授权实例应按普通授权资源稳定排序，不受所有者超级优先或降级备用影响'
  )

  const localSuperAccount = repositories.updateAuthorizedAccountBindingDispatch(granteeSuperAccount.id, { superPriorityEnabled: true }, granteeAccess)
  assert.equal(localSuperAccount?.superPriorityEnabled, true, '被授权人应能为自己的授权实例开启超级优先')
  assert.equal(bindingRow(granteeSuperAccount.id, granteeGroup.id)?.local_super_priority_enabled, 1, '授权实例超级优先应写入分组绑定')
  assert.equal(accountDispatchRow(granteeSuperAccount.id)?.super_priority_enabled, 0, '授权实例超级优先不应写入实例账户字段')
  assert.equal(repositories.listAccounts(ownerAccess).find((account) => account.id === superAccount.id)?.superPriorityEnabled, true, '实例超级优先不应修改所有者原始配置')
  assert.equal(repositories.listAccounts(otherGranteeAccess).find((account) => account.id === otherGranteeSuperAccount.id)?.superPriorityEnabled, false, '实例超级优先不应影响其他被授权人')
  assert.deepEqual(
    dispatchIds(granteeGroup.id, grantee.id, [granteeSuperAccount.id, granteeNormalAccount.id, granteeFallbackAccount.id]),
    [granteeSuperAccount.id, granteeNormalAccount.id, granteeFallbackAccount.id],
    '被授权人的实例超级优先应影响自己的分组调度'
  )

  repositories.updateAuthorizedAccountBindingDispatch(granteeSuperAccount.id, { superPriorityEnabled: false }, granteeAccess)
  const localPriorityAccount = repositories.updateAuthorizedAccountBindingDispatch(granteeFallbackAccount.id, { priority: 7 }, granteeAccess)
  assert.equal(localPriorityAccount?.priority, 7, '被授权人应能修改当前分组内授权实例优先级')
  assert.equal(bindingRow(granteeFallbackAccount.id, granteeGroup.id)?.local_priority, 7, '授权实例优先级应写入当前分组绑定')
  assert.equal(accountDispatchRow(granteeFallbackAccount.id)?.priority, 0, '授权实例优先级不应写入实例账户字段')
  assert.equal(bindingRow(otherGranteeFallbackAccount.id, otherGranteeGroup.id)?.local_priority, 0, '授权实例优先级不应影响其他被授权人的绑定')
  const localFallbackAccount = repositories.updateAuthorizedAccountBindingDispatch(granteeNormalAccount.id, { fallbackEnabled: true }, granteeAccess)
  assert.equal(localFallbackAccount?.fallbackEnabled, true, '被授权人应能为自己的授权实例开启降级备用')
  assert.equal(bindingRow(granteeNormalAccount.id, granteeGroup.id)?.local_fallback_enabled, 1, '授权实例降级备用应写入分组绑定')
  assert.equal(accountDispatchRow(granteeNormalAccount.id)?.fallback_enabled, 0, '授权实例降级备用不应写入实例账户字段')
  assert.equal(repositories.listAccounts(ownerAccess).find((account) => account.id === normalAccount.id)?.fallbackEnabled, false, '实例降级备用不应修改所有者原始配置')
  assert.equal(repositories.listAccounts(otherGranteeAccess).find((account) => account.id === otherGranteeNormalAccount.id)?.fallbackEnabled, false, '实例降级备用不应影响其他被授权人')
  assert.deepEqual(
    dispatchIds(granteeGroup.id, grantee.id, [granteeSuperAccount.id, granteeFallbackAccount.id, granteeNormalAccount.id]),
    [granteeSuperAccount.id, granteeFallbackAccount.id, granteeNormalAccount.id],
    '被授权人的实例降级备用应仅影响自己的分组调度'
  )

  const migration = repositories.migrateAccountTraffic({
    sourceAccountId: granteeNormalAccount.id,
    targetAccountId: granteeOwnedAccount.id,
    sourceStatus: 'temporary_unavailable'
  }, granteeAccess)
  assert.equal(migration?.sourceAccount.status, 'temporary_unavailable', '授权账户迁移应把源实例置为临时不可调用')
  assert.equal(migration?.sourceAccount.fallbackEnabled, true, '授权实例临时不可调用后应保留降级备用')
  assert.equal(migration?.targetAccount.id, granteeOwnedAccount.id, '授权账户迁移应允许切到被授权人自己的同分组可用账户')
  assert.equal(repositories.listAccounts(ownerAccess).find((account) => account.id === normalAccount.id)?.status, 'active', '授权账户迁移不应修改所有者原账户状态')
  assert.equal(repositories.listAccounts(otherGranteeAccess).find((account) => account.id === otherGranteeNormalAccount.id)?.status, 'active', '授权账户迁移不应影响其他被授权人')
  assert(!repositories.listOpenAIAccountsForGroup(granteeGroup.id, grantee.id).some((account) => account.id === granteeNormalAccount.id), '迁移后的授权实例不应继续参与被授权人调度')
  assert(repositories.listOpenAIAccountsForGroup(otherGranteeGroup.id, otherGrantee.id).some((account) => account.id === otherGranteeNormalAccount.id), '迁移后不应移除其他被授权人的同来源授权实例调度')
  const restored = repositories.updateAuthorizedAccountBindingDispatch(granteeNormalAccount.id, { clearFailureState: true }, granteeAccess)
  assert.equal(restored?.status, 'active', '被授权人应能恢复自己本地临时不可调用的授权账户')
  assert.equal(restored?.fallbackEnabled, true, '恢复后应继续保留被授权人的实例降级备用')
  assert(repositories.listOpenAIAccountsForGroup(granteeGroup.id, grantee.id).some((account) => account.id === granteeNormalAccount.id), '恢复后授权实例应重新参与被授权人调度')

  const granteeAccounts = repositories.listAccounts(granteeAccess)
  const granteeSuperView = granteeAccounts.find((account) => account.id === granteeSuperAccount.id)
  const granteeNormalView = granteeAccounts.find((account) => account.id === granteeNormalAccount.id)
  const granteeFallbackView = granteeAccounts.find((account) => account.id === granteeFallbackAccount.id)
  assert.equal(granteeSuperView?.superPriorityEnabled, false, '取消实例超级优先后授权账户列表不应展示超级优先')
  assert.equal(granteeNormalView?.fallbackEnabled, true, '开启实例降级备用后授权账户列表应展示降级备用')
  assert.equal(granteeFallbackView?.fallbackEnabled, false, '未开启实例降级备用的授权账户列表不应展示降级备用')

  console.log('授权账户调度标记回归通过')
} finally {
  try {
    databaseModule.getDatabase().close()
      databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function dispatchIds(groupId: string, systemAccountId: string, expectedIds: string[]): string[] {
  const expected = new Set(expectedIds)
  return repositories.listOpenAIAccountsForGroup(groupId, systemAccountId)
    .map((account) => account.id)
    .filter((id) => expected.has(id))
}

function authorizedInstanceForSource(sourceAccountId: string, access: { systemAccountId: string; role: 'user' }) {
  const account = repositories.listAccounts(access)
    .find((item) => item.authorizationInstanceSourceAccountId === sourceAccountId)
  assert(account, `被授权用户视角应能读取来源账户 ${sourceAccountId} 的授权实例`)
  return account
}

function bindingRow(accountId: string, groupId: string) {
  return databaseModule.getDatabase()
    .prepare(`
      SELECT local_priority, local_super_priority_enabled, local_fallback_enabled
      FROM group_accounts
      WHERE account_id = ? AND group_id = ? AND enabled = 1
      LIMIT 1
    `)
    .get(accountId, groupId) as { local_priority?: number; local_super_priority_enabled?: number; local_fallback_enabled?: number } | undefined
}

function accountDispatchRow(accountId: string) {
  return databaseModule.getDatabase()
    .prepare('SELECT priority, super_priority_enabled, fallback_enabled FROM accounts WHERE id = ? LIMIT 1')
    .get(accountId) as { priority?: number; super_priority_enabled?: number; fallback_enabled?: number } | undefined
}
