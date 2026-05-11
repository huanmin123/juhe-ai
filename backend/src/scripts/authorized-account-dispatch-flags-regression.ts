import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../config/runtime.js'
import { logger } from '../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorized-account-dispatch-flags-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'authorized-account-dispatch-flags.sqlite3')
runtimeConfig.secret = 'authorized-account-dispatch-flags-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../storage/database.js'),
  import('../storage/repositories.js')
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

  for (const account of [superAccount, normalAccount, fallbackAccount]) {
    repositories.createResourceAuthorization({
      resourceType: 'account',
      resourceId: account.id,
      granteeType: 'system_account',
      granteeId: grantee.id,
      remark: '调度标记回归'
    }, ownerAccess)
    repositories.createResourceAuthorization({
      resourceType: 'account',
      resourceId: account.id,
      granteeType: 'system_account',
      granteeId: otherGrantee.id,
      remark: '调度标记第二被授权人回归'
    }, ownerAccess)
  }

  const granteeGroup = repositories.createGroup({
    name: '被授权人调度分组',
    providerCode: 'openai'
  }, granteeAccess)
  const otherGranteeGroup = repositories.createGroup({
    name: '另一个被授权人调度分组',
    providerCode: 'openai'
  }, otherGranteeAccess)
  for (const account of [superAccount, normalAccount, fallbackAccount, granteeOwnedAccount]) {
    const bound = repositories.setAccountGroup(account.id, granteeGroup.id, granteeAccess)
    assert(bound, `授权账户绑定分组失败：${account.name}`)
  }
  for (const account of [superAccount, normalAccount, fallbackAccount]) {
    const bound = repositories.setAccountGroup(account.id, otherGranteeGroup.id, otherGranteeAccess)
    assert(bound, `第二被授权人绑定分组失败：${account.name}`)
  }

  const dispatchAccounts = repositories.listOpenAIAccountsForGroup(granteeGroup.id, grantee.id)
  const dispatchById = new Map(dispatchAccounts.map((account) => [account.id, account]))
  assert.equal(dispatchById.get(superAccount.id)?.superPriorityEnabled, false, '授权账户不应继承所有者超级优先')
  assert.equal(dispatchById.get(superAccount.id)?.priority, 0, '授权账户不应继承所有者优先级')
  assert.equal(dispatchById.get(fallbackAccount.id)?.fallbackEnabled, false, '授权账户不应继承所有者降级备用')
  assert.deepEqual(
    dispatchIds(granteeGroup.id, grantee.id, [normalAccount.id, superAccount.id, fallbackAccount.id]),
    [normalAccount.id, superAccount.id, fallbackAccount.id],
    '授权账户应按普通授权资源稳定排序，不受所有者超级优先或降级备用影响'
  )

  const localSuperAccount = repositories.updateAuthorizedAccountBindingDispatch(superAccount.id, { superPriorityEnabled: true }, granteeAccess)
  assert.equal(localSuperAccount?.superPriorityEnabled, true, '被授权人应能为自己的授权账户开启本地超级优先')
  assert.equal(repositories.listAccounts(ownerAccess).find((account) => account.id === superAccount.id)?.superPriorityEnabled, true, '本地超级优先不应修改所有者原始配置')
  assert.equal(repositories.listAccounts(otherGranteeAccess).find((account) => account.id === superAccount.id)?.superPriorityEnabled, false, '本地超级优先不应影响其他被授权人')
  assert.deepEqual(
    dispatchIds(granteeGroup.id, grantee.id, [superAccount.id, normalAccount.id, fallbackAccount.id]),
    [superAccount.id, normalAccount.id, fallbackAccount.id],
    '被授权人的本地超级优先应影响自己的分组调度'
  )

  repositories.updateAuthorizedAccountBindingDispatch(superAccount.id, { superPriorityEnabled: false }, granteeAccess)
  const localFallbackAccount = repositories.updateAuthorizedAccountBindingDispatch(normalAccount.id, { fallbackEnabled: true }, granteeAccess)
  assert.equal(localFallbackAccount?.fallbackEnabled, true, '被授权人应能为自己的授权账户开启本地降级备用')
  assert.equal(repositories.listAccounts(ownerAccess).find((account) => account.id === normalAccount.id)?.fallbackEnabled, false, '本地降级备用不应修改所有者原始配置')
  assert.equal(repositories.listAccounts(otherGranteeAccess).find((account) => account.id === normalAccount.id)?.fallbackEnabled, false, '本地降级备用不应影响其他被授权人')
  assert.deepEqual(
    dispatchIds(granteeGroup.id, grantee.id, [superAccount.id, fallbackAccount.id, normalAccount.id]),
    [superAccount.id, fallbackAccount.id, normalAccount.id],
    '被授权人的本地降级备用应仅影响自己的分组调度'
  )

  const migration = repositories.migrateAccountTraffic({
    sourceAccountId: fallbackAccount.id,
    targetAccountId: granteeOwnedAccount.id,
    sourceStatus: 'temporary_unavailable'
  }, granteeAccess)
  assert.equal(migration?.sourceAccount.status, 'temporary_unavailable', '授权账户迁移应把源账户置为被授权人本地临时不可调用')
  assert.equal(migration?.targetAccount.id, granteeOwnedAccount.id, '授权账户迁移应允许切到被授权人自己的同分组可用账户')
  assert.equal(repositories.listAccounts(ownerAccess).find((account) => account.id === fallbackAccount.id)?.status, 'active', '授权账户迁移不应修改所有者原账户状态')
  assert.equal(repositories.listAccounts(otherGranteeAccess).find((account) => account.id === fallbackAccount.id)?.status, 'active', '授权账户迁移不应影响其他被授权人')
  assert(!repositories.listOpenAIAccountsForGroup(granteeGroup.id, grantee.id).some((account) => account.id === fallbackAccount.id), '本地迁移后的授权账户不应继续参与被授权人调度')
  assert(repositories.listOpenAIAccountsForGroup(otherGranteeGroup.id, otherGrantee.id).some((account) => account.id === fallbackAccount.id), '本地迁移后不应移除其他被授权人的同账号调度')
  const restored = repositories.updateAuthorizedAccountBindingDispatch(fallbackAccount.id, { clearFailureState: true }, granteeAccess)
  assert.equal(restored?.status, 'active', '被授权人应能恢复自己本地临时不可调用的授权账户')
  assert(repositories.listOpenAIAccountsForGroup(granteeGroup.id, grantee.id).some((account) => account.id === fallbackAccount.id), '恢复后授权账户应重新参与被授权人调度')

  const granteeAccounts = repositories.listAccounts(granteeAccess)
  const granteeSuperAccount = granteeAccounts.find((account) => account.id === superAccount.id)
  const granteeNormalAccount = granteeAccounts.find((account) => account.id === normalAccount.id)
  const granteeFallbackAccount = granteeAccounts.find((account) => account.id === fallbackAccount.id)
  assert.equal(granteeSuperAccount?.superPriorityEnabled, false, '取消本地超级优先后授权账户列表不应展示超级优先')
  assert.equal(granteeNormalAccount?.fallbackEnabled, true, '开启本地降级备用后授权账户列表应展示降级备用')
  assert.equal(granteeFallbackAccount?.fallbackEnabled, false, '未开启本地降级备用的授权账户列表不应展示降级备用')

  console.log('授权账户调度标记回归通过')
} finally {
  try {
    databaseModule.getDatabase().close()
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
