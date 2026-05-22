import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-storage-cache-invalidation-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gateway-storage-cache-invalidation.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-storage-cache-invalidation-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, gatewayCache] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/gateway-runtime-cache.service.js')
])

try {
  const owner = repositories.createSystemAccount({
    username: 'cache_invalidation_owner',
    displayName: '缓存失效所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'cache_invalidation_grantee',
    displayName: '缓存失效被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const helperMember = repositories.createSystemAccount({
    username: 'cache_invalidation_helper',
    displayName: '缓存失效团队初始成员',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }

  const proxy = repositories.createProxy({
    name: '缓存失效代理',
    type: 'http',
    host: '127.0.0.1',
    port: 18_180,
    enabled: true
  })
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '缓存失效主账户',
    type: 'api_key',
    credentials: { api_key: 'sk-cache-invalidation-owner', base_url: 'https://api.openai.com/v1' },
    proxyProfileId: proxy.id
  }, ownerAccess)
  assert(account.boundGroupId, '新建账户应绑定默认分组')
  const apiKey = repositories.createApiKeyRecord({
    name: '缓存失效 API Key',
    groupId: account.boundGroupId
  }, ownerAccess)

  const firstRuntime = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert.deepEqual(firstRuntime.accounts.map((item) => item.id), [account.id], '首次运行配置应包含主账户')
  assert.equal(firstRuntime.accounts[0]?.proxyUrl, 'http://127.0.0.1:18180', '首次运行配置应包含代理 URL')
  assert.equal(gatewayCache.listCachedOpenAIAccountsForGroup(account.boundGroupId, owner.id)[0]?.proxyUrl, 'http://127.0.0.1:18180', '首次分组账号缓存应包含代理 URL')

  const transactionStarted = databaseModule.beginDatabaseTransaction()
  try {
    const transactionalProxy = repositories.createProxy({
      name: '缓存失效事务代理',
      type: 'http',
      host: '127.0.0.1',
      port: 18_183,
      enabled: true
    })
    repositories.updateAccount(account.id, { proxyProfileId: transactionalProxy.id }, ownerAccess)
    assert.equal(gatewayCache.listCachedOpenAIAccountsForGroup(account.boundGroupId, owner.id)[0]?.proxyUrl, 'http://127.0.0.1:18180', '事务提交前不应提前清理分组账号缓存')
    databaseModule.commitDatabaseTransaction(databaseModule.getDatabase(), transactionStarted)
  } catch (error) {
    databaseModule.rollbackDatabaseTransaction(databaseModule.getDatabase(), transactionStarted)
    throw error
  }
  assert.equal(gatewayCache.listCachedOpenAIAccountsForGroup(account.boundGroupId, owner.id)[0]?.proxyUrl, 'http://127.0.0.1:18183', '事务提交后应统一清理分组账号缓存')

  repositories.updateProxy(proxy.id, { port: 18_181 })
  repositories.updateAccount(account.id, { proxyProfileId: proxy.id }, ownerAccess)
  const afterProxyUpdate = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert.equal(afterProxyUpdate.accounts[0]?.proxyUrl, 'http://127.0.0.1:18181', '直接更新代理并切回绑定后运行配置缓存应立即刷新')

  repositories.updateSettings({ defaultTemporaryUnschedulableMinutes: 17 })
  const afterSettingsUpdate = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert.equal(afterSettingsUpdate.settings.defaultTemporaryUnschedulableMinutes, 17, '直接更新系统设置后网关设置缓存应立即刷新')

  const emptyGroup = repositories.createGroup({
    name: '缓存失效新空分组',
    providerCode: 'openai'
  }, ownerAccess)
  repositories.updateApiKey(apiKey.id, { status: 'active', groupId: emptyGroup.id }, ownerAccess)
  assert.deepEqual(await runtimeAccountIds(apiKey.key), [], '直接新建空分组并切换 API Key 后运行配置应立即使用新分组')
  repositories.updateApiKey(apiKey.id, { groupId: account.boundGroupId }, ownerAccess)
  assert.deepEqual(await runtimeAccountIds(apiKey.key), [account.id], '直接切回原分组后运行配置应立即恢复原账号')

  const lateProxy = repositories.createProxy({
    name: '缓存失效后建代理',
    type: 'http',
    host: '127.0.0.1',
    port: 18_182,
    enabled: true
  })
  repositories.updateAccount(account.id, { proxyProfileId: lateProxy.id }, ownerAccess)
  const afterLateProxyBinding = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert.equal(afterLateProxyBinding.accounts[0]?.proxyUrl, 'http://127.0.0.1:18182', '直接新建代理并绑定账号后运行配置应立即包含新代理')

  repositories.updateAccount(account.id, { status: 'disabled' }, ownerAccess)
  assert.deepEqual(await runtimeAccountIds(apiKey.key), [], '直接停用账户后候选账号缓存应立即移除该账户')
  repositories.updateAccount(account.id, { status: 'active' }, ownerAccess)
  assert.deepEqual(await runtimeAccountIds(apiKey.key), [account.id], '直接恢复账户后候选账号缓存应立即恢复该账户')

  repositories.updateApiKey(apiKey.id, { status: 'disabled' }, ownerAccess)
  const afterApiKeyDisabled = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert.equal(afterApiKeyDisabled.apiKey, undefined, '直接停用 API Key 后运行配置缓存不应继续接受旧 key')

  const granteeGroup = repositories.createGroup({
    name: '缓存失效被授权分组',
    providerCode: 'openai'
  }, granteeAccess)
  const granteeApiKey = repositories.createApiKeyRecord({
    name: '缓存失效被授权 API Key',
    groupId: granteeGroup.id
  }, granteeAccess)
  const sharedAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '缓存失效共享账户',
    type: 'api_key',
    credentials: { api_key: 'sk-cache-invalidation-shared', base_url: 'https://api.openai.com/v1' }
  }, ownerAccess)
  const accountAuthorization = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: sharedAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: '缓存失效账号授权'
  }, ownerAccess)

  assert.deepEqual(await runtimeAccountIds(granteeApiKey.key), [], '绑定前被授权分组应没有候选账号')
  assert(repositories.setAccountGroup(sharedAccount.id, granteeGroup.id, granteeAccess), '被授权账号绑定分组失败')
  assert.deepEqual(await runtimeAccountIds(granteeApiKey.key), [sharedAccount.id], '直接绑定授权账号后候选账号缓存应立即出现该账号')
  assert(repositories.revokeResourceAuthorization(accountAuthorization.id, {}, ownerAccess), '回收账号授权失败')
  assert.deepEqual(await runtimeAccountIds(granteeApiKey.key), [], '直接回收账号授权后候选账号缓存应立即移除该账号')

  const team = repositories.createSystemTeam({
    name: '缓存失效团队',
    status: 'active'
  }, adminAccess)
  assert(repositories.addSystemTeamMembers(team.id, { systemAccountIds: [helperMember.id] }, adminAccess), '添加团队初始成员失败')
  repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: account.boundGroupId,
    granteeType: 'team',
    granteeId: team.id,
    remark: '缓存失效团队分组授权'
  }, ownerAccess)
  assert.equal(gatewayCache.resolveCachedGroupUsageAccessMetadata(account.boundGroupId, grantee.id), undefined, '加入团队前应没有分组访问权限')
  const teamAfterAdd = repositories.addSystemTeamMembers(team.id, { systemAccountIds: [grantee.id] }, adminAccess)
  assert(teamAfterAdd, '添加目标团队成员失败')
  assert.equal(gatewayCache.resolveCachedGroupUsageAccessMetadata(account.boundGroupId, grantee.id)?.groupAuthorizationSourceTeamId, team.id, '直接添加团队成员后分组访问缓存应立即生效')
  const granteeMember = teamAfterAdd.members?.find((member) => member.systemAccountId === grantee.id)
  assert(granteeMember?.id, '团队成员记录应包含被授权人')
  assert(repositories.removeSystemTeamMember(team.id, granteeMember.id, adminAccess), '移除目标团队成员失败')
  assert.equal(gatewayCache.resolveCachedGroupUsageAccessMetadata(account.boundGroupId, grantee.id), undefined, '直接移除团队成员后分组访问缓存应立即失效')

  const statusOwner = repositories.createSystemAccount({
    username: 'cache_invalidation_status_owner',
    displayName: '缓存失效状态所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const statusOwnerAccess = { systemAccountId: statusOwner.id, role: 'user' as const }
  const statusAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '缓存失效状态账户',
    type: 'api_key',
    credentials: { api_key: 'sk-cache-invalidation-status', base_url: 'https://api.openai.com/v1' }
  }, statusOwnerAccess)
  assert(statusAccount.boundGroupId, '状态账户应绑定默认分组')
  const statusApiKey = repositories.createApiKeyRecord({
    name: '缓存失效状态 API Key',
    groupId: statusAccount.boundGroupId
  }, statusOwnerAccess)
  assert.deepEqual(await runtimeAccountIds(statusApiKey.key), [statusAccount.id], '停用系统账户前应可读取运行配置')
  repositories.updateSystemAccount(statusOwner.id, { status: 'disabled' })
  const afterSystemAccountDisabled = await gatewayCache.readCachedGatewayRuntimeAsync(statusApiKey.key)
  assert.equal(afterSystemAccountDisabled.apiKey, undefined, '直接停用系统账户后 API Key 校验缓存应立即失效')

  console.log('网关仓储直写缓存失效回归通过')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

async function runtimeAccountIds(apiKey: string): Promise<string[]> {
  return (await gatewayCache.readCachedGatewayRuntimeAsync(apiKey)).accounts.map((account) => account.id)
}
