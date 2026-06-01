import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-concurrency-snapshot-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'gateway-concurrency-snapshot.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-concurrency-snapshot-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  dbServiceIpc,
  runtimeSnapshot
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/db-service/db-service-ipc.js'),
  import('../../modules/gateway/gateway-runtime-snapshot.service.js')
])

let serverConcurrency: Record<string, number> = {}
const requestedScopes: Array<string | undefined> = []

class FakeServerParent extends EventEmitter {
  send(message: unknown): boolean {
    if (isServerRuntimeRequest(message)) {
      requestedScopes.push(message.scope)
      queueMicrotask(() => {
        dbServiceIpc.handleDbServiceParentRuntimeMessage({
          type: 'db_service_server_runtime_response',
          requestId: message.requestId,
          ok: true,
          result: {
            accountConcurrency: serverConcurrency
          }
        })
      })
    }
    return true
  }
}

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const grantee = repositories.createSystemAccount({
    username: 'concurrency_snapshot_grantee',
    displayName: '实时并发快照被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeTargetGroup = repositories.createGroup({
    name: '实时并发快照被授权目标分组',
    providerCode: 'openai'
  }, granteeAccess)
  const group = repositories.createGroup({
    name: '实时并发快照分组',
    providerCode: 'openai'
  }, access)
  const accountA = repositories.createAccount({
    providerCode: 'openai',
    name: '实时并发快照账号 A',
    type: 'api_key',
    credentials: {
      api_key: 'sk-concurrency-snapshot-a',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    concurrencyLimit: 10,
    schedulable: true,
    groupId: group.id
  }, access)
  const accountB = repositories.createAccount({
    providerCode: 'openai',
    name: '实时并发快照账号 B',
    type: 'api_key',
    credentials: {
      api_key: 'sk-concurrency-snapshot-b',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    concurrencyLimit: 10,
    schedulable: true,
    groupId: group.id
  }, access)
  repositories.setAccountGroup(accountA.id, group.id, access)
  repositories.setAccountGroup(accountB.id, group.id, access)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: accountA.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeTargetGroup.id,
    remark: '实时并发快照授权账户回归'
  }, access)
  const authorizedAccountA = repositories.listAccounts({ systemAccountId: grantee.id, role: 'user' as const })
    .find((account) => account.authorizationInstanceSourceAccountId === accountA.id)
  assert(authorizedAccountA?.id, '被授权用户应看到授权实例账户')

  serverConcurrency = {
    [accountA.id]: 2,
    [accountB.id]: 1,
    [authorizedAccountA.id]: 4
  }

  const fakeParent = new FakeServerParent()
  const previousProcessRole = runtimeConfig.processRole
  const previousSend = process.send
  try {
    ;(process as typeof process & { send?: (message: unknown) => boolean }).send = fakeParent.send.bind(fakeParent)
    runtimeConfig.processRole = 'db-service'

    const dbServiceLocalAccountPage = repositories.listAccountsPage(access, { page: 1, pageSize: 20 })
    assert.equal(findAccount(dbServiceLocalAccountPage.items, accountA.id).currentConcurrency, 0, 'DB service 本地并发快照应为空')

    const accountPage = repositories.listAccountsPage(access, { page: 1, pageSize: 20 })
    const accountPageWithRuntime = await runtimeSnapshot.applyServerAccountConcurrencyToAccountList(accountPage)
    assert.equal(findAccount(accountPageWithRuntime.items, accountA.id).currentConcurrency, 2, '账户列表应合并 server 当前并发 A')
    assert.equal(findAccount(accountPageWithRuntime.items, accountA.id).currentConcurrencyAvailable, true, '账户列表应标记 server 并发快照可用')
    assert.equal(findAccount(accountPageWithRuntime.items, accountB.id).currentConcurrency, 1, '账户列表应合并 server 当前并发 B')
    assert.equal(accountPageWithRuntime.runtimeSnapshot.accountConcurrencyAvailable, true, '账户分页结果应标记 server 并发快照可用')

    const granteeAccountPage = repositories.listAccountsPage({ systemAccountId: grantee.id, role: 'user' as const }, { page: 1, pageSize: 20 })
    const authorizedAccountPageWithRuntime = await runtimeSnapshot.applyServerAccountConcurrencyToAccountList(granteeAccountPage)
    const authorizedAccount = findAccount(authorizedAccountPageWithRuntime.items, authorizedAccountA.id)
    assert.equal(authorizedAccount.accessType, 'authorized', '被授权用户账户列表应返回授权账户视角')
    assert.equal(authorizedAccount.concurrencyLimit, 10, '授权账户应展示来源账号并发上限')
    assert.equal(authorizedAccount.currentConcurrency, 4, '授权实例账户应按自己的账户 ID 合并 server 当前并发')
    assert.equal(authorizedAccount.currentConcurrencyAvailable, true, '授权账户应标记 server 并发快照可用')
    assert.equal(authorizedAccountPageWithRuntime.runtimeSnapshot.accountConcurrencyAvailable, true, '仅包含授权账户时也应标记 server 并发快照可用')

    const groups = await runtimeSnapshot.applyServerAccountConcurrencyToGroups(repositories.listGroups(access))
    const targetGroup = groups.find((item) => item.id === group.id)
    assert(targetGroup, '测试分组应存在')
    assert.equal(targetGroup.accountStats.currentConcurrency, 3, '分组列表应汇总 server 当前并发')
    assert.equal(targetGroup.accountStats.currentConcurrencyAvailable, true, '分组列表应标记 server 并发快照可用')
    assert.equal(requestedScopes.length, 3, '管理账户列表、授权账户列表和分组列表应各请求一次 server 并发快照')
    assert.deepEqual(requestedScopes, ['account_runtime', 'account_runtime', 'account_concurrency'], '系统 API 应按账户运行态和分组并发分别请求轻量快照')
  } finally {
    runtimeConfig.processRole = previousProcessRole
    ;(process as typeof process & { send?: (message: unknown) => boolean }).send = previousSend
    serverConcurrency = {}
    requestedScopes.length = 0
  }

  console.log('网关实时并发快照回归通过：系统 API 通过 server 快照合并账户和分组当前并发')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function findAccount<T extends { id: string }>(accounts: T[], accountId: string): T {
  const account = accounts.find((item) => item.id === accountId)
  assert(account, `账户不存在：${accountId}`)
  return account
}

function isServerRuntimeRequest(value: unknown): value is { type: 'db_service_server_runtime_request'; requestId: string; scope?: string } {
  return typeof value === 'object'
    && value !== null
    && !Array.isArray(value)
    && (value as Record<string, unknown>).type === 'db_service_server_runtime_request'
    && typeof (value as Record<string, unknown>).requestId === 'string'
}
