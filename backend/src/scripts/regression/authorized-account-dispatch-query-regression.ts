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
    accountIds.push(account.id)
    if (index === 0) {
      disabledProxyAccountId = account.id
    }
    repositories.createResourceAuthorization({
      resourceType: 'account',
      resourceId: account.id,
      granteeType: 'system_account',
      granteeId: grantee.id,
      remark: '调度授权查询回归'
    }, ownerAccess)
    const bound = repositories.setAccountGroup(account.id, granteeGroup.id, granteeAccess)
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
    remark: '调度授权失效凭据回归'
  }, ownerAccess)
  assert(repositories.setAccountGroup(staleAuthorizedAccount.id, granteeGroup.id, granteeAccess), '失效授权账户绑定分组失败')
  assert(repositories.revokeResourceAuthorization(staleAuthorization.id, {}, ownerAccess), '失效授权回收失败')
  databaseModule.getDatabase()
    .prepare('UPDATE accounts SET credentials_encrypted = ? WHERE id = ?')
    .run('not-a-valid-encrypted-payload', staleAuthorizedAccount.id)
  databaseModule.getDatabase()
    .prepare('UPDATE proxy_profiles SET password_encrypted = ? WHERE id = ?')
    .run('not-a-valid-encrypted-proxy-payload', staleBadProxy.id)
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
      if (/\bresource_id\s+IN\s*\(/i.test(sql)) {
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
    assert.equal(dispatchIds.has(staleAuthorizedAccount.id), false, '授权失效账户应在凭据解密前被跳过')
    assert(!dispatchAccounts.some((account) => account.proxyProfileId === staleBadProxy.id), '授权失效账户的坏代理不应进入代理解析范围')
    const enabledProxyAccounts = dispatchAccounts.filter((account) => account.id !== disabledProxyAccountId)
    assert(enabledProxyAccounts.every((account) => account.proxyUrl === 'http://dispatch_proxy_user:dispatch_proxy_password@127.0.0.1:18080'), '共用代理账户应解析出代理 URL')
    const disabledProxyAccount = dispatchAccounts.find((account) => account.id === disabledProxyAccountId)
    assert.equal(disabledProxyAccount?.proxyProfileUnavailable, true, '停用代理账户应保留代理不可用标记')
    assert.equal(batchProxyProfileSelects, 1, '授权账户调度应批量读取代理配置')
    assert.equal(proxyProfileSelects, 1, '授权账户调度代理查询数应保持常量')
    assert.equal(batchResourceAuthorizationSelects, 1, '授权账户调度应批量读取账号授权')
    assert.equal(singleResourceAuthorizationSelects, 0, '授权账户调度不应逐账号读取账号授权')
    assert.equal(resourceAuthorizationSelects, 1, '授权账户调度授权查询数应保持常量')
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
