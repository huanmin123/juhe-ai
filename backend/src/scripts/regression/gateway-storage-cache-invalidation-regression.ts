import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-storage-cache-invalidation-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gateway-storage-cache-invalidation.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-storage-cache-invalidation-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, gatewayCache] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js')
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
  const ownerGroup = repositories.createGroup({
    name: '缓存失效所有者分组',
    providerCode: 'gpt',
  }, ownerAccess)

  const proxy = repositories.createProxy({
    name: '缓存失效代理',
    type: 'http',
    host: '127.0.0.1',
    port: 18_180,
    enabled: true
  }, ownerAccess)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '缓存失效主账户',
    type: 'api_key',
    status: 'active',
    groupId: ownerGroup.id,
    credentials: { api_key: 'sk-cache-invalidation-owner', base_url: 'https://api.openai.com/v1' },
    proxyProfileId: proxy.id
  }, ownerAccess)
  const ownerGroupId = ownerGroup.id
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '缓存失效 API Key',
    groupBindings: [{ groupId: ownerGroupId, priority: 1, status: 'active' }],
  }, ownerAccess)

  const firstRuntime = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert.deepEqual(firstRuntime.accounts.map((item) => item.id), [account.id], '首次运行配置应包含主账户')
  assert.equal(firstRuntime.accounts[0]?.proxyUrl, 'http://127.0.0.1:18180', '首次运行配置应包含代理 URL')
  assert.equal(gatewayCache.listCachedOpenAIAccountsForGroup(ownerGroupId, owner.id)[0]?.proxyUrl, 'http://127.0.0.1:18180', '首次分组账号缓存应包含代理 URL')

  const transactionStarted = databaseModule.beginDatabaseTransaction()
  try {
    const transactionalProxy = repositories.createProxy({
      name: '缓存失效事务代理',
      type: 'http',
      host: '127.0.0.1',
      port: 18_183,
      enabled: true
    }, ownerAccess)
    repositories.updateAccount(account.id, { proxyProfileId: transactionalProxy.id }, ownerAccess)
    assert.equal(gatewayCache.listCachedOpenAIAccountsForGroup(ownerGroupId, owner.id)[0]?.proxyUrl, 'http://127.0.0.1:18180', '事务提交前不应提前清理分组账号缓存')
    databaseModule.commitDatabaseTransaction(databaseModule.getBusinessDatabase(), transactionStarted)
  } catch (error) {
    databaseModule.rollbackDatabaseTransaction(databaseModule.getBusinessDatabase(), transactionStarted)
    throw error
  }
  assert.equal(gatewayCache.listCachedOpenAIAccountsForGroup(ownerGroupId, owner.id)[0]?.proxyUrl, 'http://127.0.0.1:18183', '事务提交后应统一清理分组账号缓存')

  repositories.updateProxy(proxy.id, { port: 18_181 })
  repositories.updateAccount(account.id, { proxyProfileId: proxy.id }, ownerAccess)
  const afterProxyUpdate = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert.equal(afterProxyUpdate.accounts[0]?.proxyUrl, 'http://127.0.0.1:18181', '直接更新代理并切回绑定后运行配置缓存应立即刷新')

  repositories.updateSettings({ defaultTemporaryUnschedulableMinutes: 17 })
  const afterSettingsUpdate = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert.equal(afterSettingsUpdate.settings.defaultTemporaryUnschedulableMinutes, 17, '直接更新系统设置后网关设置缓存应立即刷新')

  const emptyGroup = repositories.createGroup({
    name: '缓存失效新空分组',
    providerCode: 'gpt',
  }, ownerAccess)
  repositories.updateRouteStrategy(apiKey.routeStrategyId, {
    groupBindings: [{ groupId: emptyGroup.id, priority: 1, status: 'active' }]
  }, ownerAccess)
  assert.deepEqual(await runtimeAccountIds(apiKey.key), [], '直接新建空分组并切换 API Key 后运行配置应立即使用新分组')
  repositories.updateRouteStrategy(apiKey.routeStrategyId, {
    groupBindings: [{ groupId: ownerGroupId, priority: 1, status: 'active' }]
  }, ownerAccess)
  assert.deepEqual(await runtimeAccountIds(apiKey.key), [account.id], '直接切回原分组后运行配置应立即恢复原账号')

  const lateProxy = repositories.createProxy({
    name: '缓存失效后建代理',
    type: 'http',
    host: '127.0.0.1',
    port: 18_182,
    enabled: true
  }, ownerAccess)
  repositories.updateAccount(account.id, { proxyProfileId: lateProxy.id }, ownerAccess)
  const afterLateProxyBinding = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert.equal(afterLateProxyBinding.accounts[0]?.proxyUrl, 'http://127.0.0.1:18182', '直接新建代理并绑定账号后运行配置应立即包含新代理')

  repositories.updateAccount(account.id, { status: 'disabled' }, ownerAccess)
  assert.deepEqual(await runtimeAccountIds(apiKey.key), [], '直接停用账户后候选账号缓存应立即移除该账户')
  repositories.updateAccount(account.id, { status: 'active' }, ownerAccess)
  assert.deepEqual(await runtimeAccountIds(apiKey.key), [account.id], '直接恢复账户后候选账号缓存应立即恢复该账户')

  repositories.updateApiKey(apiKey.id, { status: 'disabled' }, ownerAccess)
  const afterApiKeyDisabled = await gatewayCache.readCachedGatewayRuntimeAsync(apiKey.key)
  assert.equal(afterApiKeyDisabled.apiKey, undefined, '直接停用 API Key 后运行配置缓存不应继续接受已停用 key')

  const groupAuthorization = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: ownerGroup.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: '缓存失效分组授权'
  }, ownerAccess)
  const groupAuthorizationRuntimeRow = databaseModule.getBusinessDatabase()
    .prepare("SELECT id FROM resource_authorizations WHERE resource_type = 'group' AND resource_id = ? AND grantee_system_account_id = ? LIMIT 1")
    .get(ownerGroup.id, grantee.id) as { id?: string } | undefined
  assert(groupAuthorizationRuntimeRow?.id, '回归需要最终用户分组授权主记录 ID')
  const granteeAuthorizedGroupApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '缓存失效授权分组 API Key',
    groupBindings: [{ groupId: ownerGroup.id, priority: 1, status: 'active' }],
  }, granteeAccess)
  const authorizedGroupRuntime = await gatewayCache.readCachedGatewayRuntimeAsync(granteeAuthorizedGroupApiKey.key)
  assert.deepEqual(authorizedGroupRuntime.accounts.map((item) => item.id), [account.id], 'API Key 直接绑定授权分组后运行配置应读取授权方分组账号')
  assert.equal(authorizedGroupRuntime.groupAccess?.groupAccessType, 'authorized', 'API Key 直接绑定授权分组后运行配置应携带授权分组访问类型')
  assert.equal(authorizedGroupRuntime.groupAccess?.groupAuthorizationId, groupAuthorizationRuntimeRow.id, 'API Key 直接绑定授权分组后运行配置应携带最终用户分组授权 ID')
  assert(repositories.revokeResourceAuthorization(groupAuthorization.id, ownerAccess), '回收分组授权失败')
  assert.deepEqual(await runtimeAccountIds(granteeAuthorizedGroupApiKey.key), [], '直接回收分组授权后绑定该授权分组的 API Key 不应继续返回候选账号')
  const granteeGroup = repositories.createGroup({
    name: '缓存失效被授权分组',
    providerCode: 'gpt',
  }, granteeAccess)
  const replacedRevokedGroupBindingRouteStrategy = repositories.updateRouteStrategy(granteeAuthorizedGroupApiKey.routeStrategyId, {
    groupBindings: [
      { groupId: granteeGroup.id, priority: 1, status: 'active' }
    ],
  }, granteeAccess)
  assert(replacedRevokedGroupBindingRouteStrategy, '授权回收后应允许策略路由切换到当前用户自己的分组')
  assert.deepEqual(await runtimeAccountIds(granteeAuthorizedGroupApiKey.key), [], '切换到空分组后运行配置仍不应返回候选账号')

  const granteeApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '缓存失效被授权 API Key',
    groupBindings: [{ groupId: granteeGroup.id, priority: 1, status: 'active' }],
  }, granteeAccess)
  const sharedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '缓存失效共享账户',
    type: 'api_key',
    status: 'active',
    groupId: ownerGroup.id,
    credentials: { api_key: 'sk-cache-invalidation-shared', base_url: 'https://api.openai.com/v1' }
  }, ownerAccess)
  const accountAuthorization = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: sharedAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '缓存失效账号授权'
  }, ownerAccess)
  const sharedAuthorizedInstance = authorizedInstanceForSource(sharedAccount.id, granteeAccess)

  assert.deepEqual(await runtimeAccountIds(granteeApiKey.key), [sharedAuthorizedInstance.id], '授权创建时绑定目标分组后候选账号缓存应立即出现该账号')
  const sharedRuntimeBeforeSourceUpdate = await gatewayCache.readCachedGatewayRuntimeAsync(granteeApiKey.key)
  assert.equal(sharedRuntimeBeforeSourceUpdate.accounts[0]?.apiKey, 'sk-cache-invalidation-shared', '授权实例运行配置应读取父账户初始凭据')
  assert.equal(
    gatewayCache.listCachedOpenAIAccountsForGroup(granteeGroup.id, grantee.id)[0]?.apiKey,
    'sk-cache-invalidation-shared',
    '授权实例分组账号缓存应读取父账户初始凭据'
  )
  repositories.updateAccount(sharedAccount.id, {
    credentials: {
      api_key: 'sk-cache-invalidation-shared-updated',
      base_url: 'https://cache-source-updated.example/v1'
    },
    supportedModels: ['gpt-5.5']
  }, ownerAccess)
  const sharedRuntimeAfterSourceUpdate = await gatewayCache.readCachedGatewayRuntimeAsync(granteeApiKey.key)
  const sharedRuntimeAccount = sharedRuntimeAfterSourceUpdate.accounts.find((item) => item.id === sharedAuthorizedInstance.id)
  assert.equal(sharedRuntimeAccount?.apiKey, 'sk-cache-invalidation-shared-updated', '父账户 API Key 更新后授权实例运行配置缓存应立即刷新')
  assert.equal(sharedRuntimeAccount?.baseUrl, 'https://cache-source-updated.example/v1', '父账户 base_url 更新后授权实例运行配置缓存应立即刷新')
  assert.deepEqual(sharedRuntimeAccount?.supportedModels, ['gpt-5.5'], '父账户模型更新后授权实例运行配置缓存应立即刷新')
  const sharedGroupCacheAccount = gatewayCache.listCachedOpenAIAccountsForGroup(granteeGroup.id, grantee.id).find((item) => item.id === sharedAuthorizedInstance.id)
  assert.equal(sharedGroupCacheAccount?.apiKey, 'sk-cache-invalidation-shared-updated', '父账户凭据更新后授权实例分组账号缓存应立即刷新')
  assert(repositories.revokeResourceAuthorization(accountAuthorization.id, ownerAccess), '回收账号授权失败')
  assert.deepEqual(await runtimeAccountIds(granteeApiKey.key), [], '直接回收账号授权后候选账号缓存应立即移除该账号')

  const team = repositories.createSystemTeam({
    name: '缓存失效团队',
    status: 'active'
  }, adminAccess)
  assert(repositories.addSystemTeamMembers(team.id, { systemAccountIds: [helperMember.id] }, adminAccess), '添加团队初始成员失败')
  const sourceGroupId = account.boundGroupId
  assert(sourceGroupId, '源账号应包含绑定分组')
  repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: sourceGroupId,
    granteeType: 'team',
    granteeId: team.id,
    remark: '缓存失效团队分组授权'
  }, ownerAccess)
  assert.equal(gatewayCache.resolveCachedGroupUsageAccessMetadata(sourceGroupId, grantee.id), undefined, '加入团队前应没有分组访问权限')
  const teamAfterAdd = repositories.addSystemTeamMembers(team.id, { systemAccountIds: [grantee.id] }, adminAccess)
  assert(teamAfterAdd, '添加目标团队成员失败')
  assert.equal(gatewayCache.resolveCachedGroupUsageAccessMetadata(sourceGroupId, grantee.id)?.groupAuthorizationSourceTeamId, team.id, '直接添加团队成员后分组访问缓存应立即生效')
  const granteeMember = teamAfterAdd.members?.find((member) => member.systemAccountId === grantee.id)
  assert(granteeMember?.id, '团队成员记录应包含被授权人')
  assert(repositories.removeSystemTeamMember(team.id, granteeMember.id, adminAccess), '移除目标团队成员失败')
  assert.equal(gatewayCache.resolveCachedGroupUsageAccessMetadata(sourceGroupId, grantee.id), undefined, '直接移除团队成员后分组访问缓存应立即失效')

  const statusOwner = repositories.createSystemAccount({
    username: 'cache_invalidation_status_owner',
    displayName: '缓存失效状态所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const statusOwnerAccess = { systemAccountId: statusOwner.id, role: 'user' as const }
  const statusGroup = repositories.createGroup({
    name: '缓存失效状态分组',
    providerCode: 'gpt',
  }, statusOwnerAccess)
  const statusAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '缓存失效状态账户',
    type: 'api_key',
    status: 'active',
    groupId: statusGroup.id,
    credentials: { api_key: 'sk-cache-invalidation-status', base_url: 'https://api.openai.com/v1' }
  }, statusOwnerAccess)
  const statusGroupId = statusGroup.id
  const statusApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '缓存失效状态 API Key',
    groupBindings: [{ groupId: statusGroupId, priority: 1, status: 'active' }],
  }, statusOwnerAccess)
  assert.deepEqual(await runtimeAccountIds(statusApiKey.key), [statusAccount.id], '停用系统账户前应可读取运行配置')
  repositories.updateSystemAccount(statusOwner.id, { status: 'disabled' })
  const afterSystemAccountDisabled = await gatewayCache.readCachedGatewayRuntimeAsync(statusApiKey.key)
  assert.equal(afterSystemAccountDisabled.apiKey, undefined, '直接停用系统账户后 API Key 校验缓存应立即失效')

  console.log('网关仓储直写缓存失效回归通过')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

async function runtimeAccountIds(apiKey: string): Promise<string[]> {
  return (await gatewayCache.readCachedGatewayRuntimeAsync(apiKey)).accounts.map((account) => account.id)
}

function authorizedInstanceForSource(sourceAccountId: string, access: { systemAccountId: string; role: 'user' }) {
  const account = repositories.listAccounts(access)
    .find((item) => item.authorizationInstanceSourceAccountId === sourceAccountId)
  assert(account, `被授权用户视角应能读取来源账户 ${sourceAccountId} 的授权实例`)
  return account
}
