import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorized-account-dispatch-query-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'authorized-account-dispatch-query.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'authorized-account-dispatch-query-records.sqlite3')
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

  const accountCount = 40
  const accountIds: string[] = []
  for (let index = 0; index < accountCount; index += 1) {
    const account = repositories.createAccount({
      providerCode: 'openai',
      name: `授权调度查询账户 ${String(index).padStart(2, '0')}`,
      type: 'api_key',
      credentials: { api_key: `sk-authorized-dispatch-query-${index}`, base_url: 'https://api.openai.com/v1' }
    }, ownerAccess)
    accountIds.push(account.id)
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

  const database = databaseModule.getDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  let resourceAuthorizationSelects = 0
  let singleResourceAuthorizationSelects = 0
  let batchResourceAuthorizationSelects = 0
  database.prepare = ((sql: string) => {
    if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+resource_authorizations\b/i.test(sql)) {
      resourceAuthorizationSelects += 1
      if (/\bresource_id\s+IN\s*\(/i.test(sql)) {
        batchResourceAuthorizationSelects += 1
      } else {
        singleResourceAuthorizationSelects += 1
      }
    }
    return originalPrepare(sql)
  }) as typeof database.prepare

  try {
    const dispatchAccounts = repositories.listOpenAIAccountsForGroup(granteeGroup.id, grantee.id)
    const dispatchIds = new Set(dispatchAccounts.map((account) => account.id))
    for (const accountId of accountIds) {
      assert(dispatchIds.has(accountId), `授权账户缺失调度候选：${accountId}`)
    }
    assert.equal(batchResourceAuthorizationSelects, 1, '授权账户调度应批量读取账号授权')
    assert.equal(singleResourceAuthorizationSelects, 0, '授权账户调度不应逐账号读取账号授权')
    assert.equal(resourceAuthorizationSelects, 1, '授权账户调度授权查询数应保持常量')
  } finally {
    database.prepare = originalPrepare
  }

  console.log('授权账户调度授权批量查询回归通过')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
