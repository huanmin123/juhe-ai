import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorized-account-name-sync-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorized-account-name-sync-secret'
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
    username: 'authorized_name_sync_owner',
    displayName: '授权名称同步所有者',
    password: 'Password-123456',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'authorized_name_sync_grantee',
    displayName: '授权名称同步被授权人',
    password: 'Password-123456',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const targetGroup = repositories.createGroup({
    name: '授权名称同步目标分组',
    providerCode: 'gpt',
    enabled: true
  }, granteeAccess)
  const ownerSourceGroup = repositories.createGroup({
    name: '授权名称同步来源分组',
    providerCode: 'gpt',
    enabled: true
  }, ownerAccess)
  const sourceAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '授权名称同步初始名',
    type: 'api_key',
    credentials: {
      api_key: 'sk-authorized-name-sync-source',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: ownerSourceGroup.id
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: sourceAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: targetGroup.id,
    remark: '授权实例名称同步回归'
  }, ownerAccess)
  const authorizedInstance = repositories.listAccounts(granteeAccess)
    .find((account) => account.authorizationInstanceSourceAccountId === sourceAccount.id)
  assert(authorizedInstance?.id, '账户授权应为被授权人创建授权实例账户')
  assert.equal(authorizedInstance.name, '授权名称同步初始名', '授权实例初始名称应来自来源账户')

  repositories.updateAccount(sourceAccount.id, { name: '授权名称同步当前名' }, ownerAccess)
  const renamedInstance = repositories.listAccountOptions(granteeAccess, {
    keyword: '授权名称同步当前名',
    limit: 10
  }).find((account) => account.id === authorizedInstance.id)
  assert.equal(renamedInstance?.name, '授权名称同步当前名', '来源账户改名后授权实例下拉应显示当前名称')
  const staleOptions = repositories.listAccountOptions(granteeAccess, {
    keyword: '授权名称同步初始名',
    limit: 10
  })
  assert.equal(staleOptions.some((account) => account.id === authorizedInstance.id), false, '来源账户改名后初始名称不应继续命中授权实例下拉')

  console.log('授权实例名称同步回归通过：来源账户改名后下拉选项使用当前名称')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}
