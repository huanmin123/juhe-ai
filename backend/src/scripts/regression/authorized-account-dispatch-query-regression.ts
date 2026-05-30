import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorized-account-dispatch-query-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'authorized-account-dispatch-query.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorized-account-dispatch-query-secret'
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
    username: 'dispatch_query_owner',
    displayName: '调度查询所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'dispatch_query_grantee',
    displayName: '调度查询被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeGroup = repositories.createGroup({
    name: '被授权人调度查询分组',
    providerCode: 'openai'
  }, granteeAccess)
  const sharedProxy = repositories.createProxy({
    name: '授权调度查询共用代理',
    type: 'http',
    host: '127.0.0.1',
    port: 18_080,
    username: 'dispatch_proxy_user',
    password: 'dispatch_proxy_password',
    enabled: true
  })
  const disabledProxy = repositories.createProxy({
    name: '授权调度查询停用代理',
    type: 'http',
    host: '127.0.0.1',
    port: 18_081,
    enabled: true
  })
  const staleBadProxy = repositories.createProxy({
    name: '授权失效账户坏代理',
    type: 'http',
    host: '127.0.0.1',
    port: 18_082,
    username: 'stale_bad_proxy_user',
    password: 'stale_bad_proxy_password',
    enabled: true
  })
  const disabledSourceBadProxy = repositories.createProxy({
    name: '授权父账户停用隔离坏代理',
    type: 'http',
    host: '127.0.0.1',
    port: 18_083,
    username: 'disabled_source_bad_proxy_user',
    password: 'disabled_source_bad_proxy_password',
    enabled: true
  })

  const accountCount = 40
  const accountIds: string[] = []
  let disabledProxyAccountId = ''
  for (let index = 0; index < accountCount; index += 1) {
    const account = repositories.createAccount({
      providerCode: 'openai',
      name: `授权调度查询账户 ${String(index).padStart(2, '0')}`,
      type: 'api_key',
      credentials: { api_key: `sk-authorized-dispatch-query-${index}`, base_url: 'https://api.openai.com/v1' },
      proxyProfileId: index === 0 ? disabledProxy.id : sharedProxy.id
    }, ownerAccess)
    repositories.createResourceAuthorization({
      resourceType: 'account',
      resourceId: account.id,
      granteeType: 'system_account',
      granteeId: grantee.id,
      targetGroupId: granteeGroup.id,
      remark: '调度授权查询回归'
    }, ownerAccess)
    const authorizedInstance = authorizedInstanceForSource(account.id, granteeAccess)
    accountIds.push(authorizedInstance.id)
    if (index === 0) {
      disabledProxyAccountId = authorizedInstance.id
    }
    const bound = repositories.setAccountGroup(authorizedInstance.id, granteeGroup.id, granteeAccess)
    assert(bound, `授权账户绑定分组失败：${account.name}`)
  }
  const staleAuthorizedAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '授权已失效且凭据损坏账户',
    type: 'api_key',
    credentials: { api_key: 'sk-authorized-dispatch-stale', base_url: 'https://api.openai.com/v1' },
    proxyProfileId: staleBadProxy.id
  }, ownerAccess)
  const staleAuthorization = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: staleAuthorizedAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '调度授权失效凭据回归'
  }, ownerAccess)
  const staleAuthorizedInstance = authorizedInstanceForSource(staleAuthorizedAccount.id, granteeAccess)
  assert(repositories.setAccountGroup(staleAuthorizedInstance.id, granteeGroup.id, granteeAccess), '失效授权实例账户绑定分组失败')
  assert(repositories.revokeResourceAuthorization(staleAuthorization.id, {}, ownerAccess), '失效授权回收失败')
  databaseModule.getDatabase()
    .prepare('UPDATE accounts SET credentials_encrypted = ? WHERE id = ?')
    .run('not-a-valid-encrypted-payload', staleAuthorizedInstance.id)
  if (staleAuthorizedInstance.proxyProfileId) {
    databaseModule.getDatabase()
      .prepare('UPDATE proxy_profiles SET password_encrypted = ? WHERE id = ?')
      .run('not-a-valid-encrypted-proxy-payload', staleAuthorizedInstance.proxyProfileId)
  }
  const disabledSourceAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '授权父账户停用且凭据损坏账户',
    type: 'api_key',
    credentials: { api_key: 'sk-authorized-dispatch-disabled-source', base_url: 'https://api.openai.com/v1' },
    proxyProfileId: disabledSourceBadProxy.id
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: disabledSourceAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '授权父账户停用隔离回归'
  }, ownerAccess)
  const ownerDisabledAuthorizedInstance = authorizedInstanceForSource(disabledSourceAccount.id, granteeAccess)
  assert(repositories.setAccountGroup(ownerDisabledAuthorizedInstance.id, granteeGroup.id, granteeAccess), '父账户停用隔离授权实例绑定分组失败')
  assert.equal(repositories.updateAccount(disabledSourceAccount.id, { status: 'disabled' }, ownerAccess)?.status, 'disabled', '父账户应能被所有者停用')
  assert(repositories.updateAccount(disabledSourceAccount.id, {
    credentials: {
      api_key: 'sk-authorized-dispatch-disabled-source-updated',
      base_url: 'https://updated-owner.example/v1'
    },
    supportedModels: ['gpt-5.5']
  }, ownerAccess), '父账户停用后仍应允许所有者更新资源凭据和模型')
  repositories.updateProxy(disabledProxy.id, { enabled: false })

  const database = databaseModule.getDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  let resourceAuthorizationSelects = 0
  let singleResourceAuthorizationSelects = 0
  let batchResourceAuthorizationSelects = 0
  let proxyProfileSelects = 0
  let batchProxyProfileSelects = 0
  let accountUpdateStatements = 0
  database.prepare = ((sql: string) => {
    if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+resource_authorizations\b/i.test(sql)) {
      resourceAuthorizationSelects += 1
      if (/\b(?:id|resource_id)\s+IN\s*\(/i.test(sql)) {
        batchResourceAuthorizationSelects += 1
      } else {
        singleResourceAuthorizationSelects += 1
      }
    }
    if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+proxy_profiles\b/i.test(sql)) {
      proxyProfileSelects += 1
      if (/\bid\s+IN\s*\(/i.test(sql)) {
        batchProxyProfileSelects += 1
      }
    }
    if (/^\s*UPDATE\s+accounts\b/i.test(sql)) {
      accountUpdateStatements += 1
    }
    return originalPrepare(sql)
  }) as typeof database.prepare

  try {
    const dispatchAccounts = repositories.listOpenAIAccountsForGroup(granteeGroup.id, grantee.id)
    const dispatchIds = new Set(dispatchAccounts.map((account) => account.id))
    for (const accountId of accountIds) {
      assert(dispatchIds.has(accountId), `授权账户缺失调度候选：${accountId}`)
    }
    assert.equal(dispatchIds.has(staleAuthorizedInstance.id), false, '授权关系失效后实例应在凭据解密前被跳过')
    assert.equal(dispatchIds.has(ownerDisabledAuthorizedInstance.id), true, '父账户停用不应阻断授权实例调度')
    assert(!dispatchAccounts.some((account) => account.proxyProfileId === staleBadProxy.id || account.proxyProfileId === staleAuthorizedInstance.proxyProfileId), '授权失效实例的坏代理不应进入代理解析范围')
    const enabledProxyAccounts = dispatchAccounts.filter((account) => accountIds.includes(account.id) && account.id !== disabledProxyAccountId)
    assert(enabledProxyAccounts.every((account) => account.proxyUrl === 'http://dispatch_proxy_user:dispatch_proxy_password@127.0.0.1:18080'), '共用代理账户应解析出代理 URL')
    const disabledProxyAccount = dispatchAccounts.find((account) => account.id === disabledProxyAccountId)
    assert.equal(disabledProxyAccount?.proxyProfileUnavailable, true, '父账户代理停用应同步到授权实例运行时')
    assert.equal(disabledProxyAccount?.proxyUrl, undefined, '授权实例不应继续使用旧克隆代理配置')
    const ownerDisabledDispatchAccount = dispatchAccounts.find((account) => account.id === ownerDisabledAuthorizedInstance.id)
    assert.equal(ownerDisabledDispatchAccount?.apiKey, 'sk-authorized-dispatch-disabled-source-updated', '父账户 API Key 更新后授权实例运行时应读取父账户最新凭据')
    assert.equal(ownerDisabledDispatchAccount?.baseUrl, 'https://updated-owner.example/v1', '父账户 base_url 更新后授权实例运行时应同步')
    assert.deepEqual(ownerDisabledDispatchAccount?.supportedModels, ['gpt-5.5'], '父账户支持模型更新后授权实例运行时应同步')
    assert.equal(ownerDisabledDispatchAccount?.proxyUrl, 'http://disabled_source_bad_proxy_user:disabled_source_bad_proxy_password@127.0.0.1:18083', '父账户状态停用不应阻断授权实例读取父资源代理')
    assert.equal(batchProxyProfileSelects, 1, '授权账户调度应批量读取代理配置')
    assert.equal(proxyProfileSelects, 1, '授权账户调度代理查询数应保持常量')
    assert.equal(batchResourceAuthorizationSelects, 1, '授权账户调度应批量读取账号授权')
    assert.equal(singleResourceAuthorizationSelects, 1, '授权实例同步只允许一次固定授权扫描，不应逐账号读取账号授权')
    assert.equal(resourceAuthorizationSelects, 2, '授权账户调度授权查询数应保持常量')
    assert.equal(accountUpdateStatements, 0, '授权账户调度读路径不应写 accounts 或刷新统计')
  } finally {
    database.prepare = originalPrepare
  }

  console.log('授权账户调度授权批量查询回归通过')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function authorizedInstanceForSource(sourceAccountId: string, access: { systemAccountId: string; role: 'user' }) {
  const account = repositories.listAccounts(access)
    .find((item) => item.authorizationInstanceSourceAccountId === sourceAccountId)
  assert(account, `被授权用户视角应能读取来源账户 ${sourceAccountId} 的授权实例`)
  return account
}
