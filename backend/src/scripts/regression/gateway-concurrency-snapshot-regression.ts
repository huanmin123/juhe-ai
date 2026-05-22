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
  const accountA = repositories.createAccount({
    providerCode: 'openai',
    name: '实时并发快照账号 A',
    type: 'api_key',
    credentials: {
      api_key: 'sk-concurrency-snapshot-a',
      base_url: 'http://127.0.0.1:9/v1'
    },
    status: 'active',
    concurrencyLimit: 10,
    schedulable: true
  }, access)
  const accountB = repositories.createAccount({
    providerCode: 'openai',
    name: '实时并发快照账号 B',
    type: 'api_key',
    credentials: {
      api_key: 'sk-concurrency-snapshot-b',
      base_url: 'http://127.0.0.1:9/v1'
    },
    status: 'active',
    concurrencyLimit: 10,
    schedulable: true
  }, access)
  const group = repositories.createGroup({
    name: '实时并发快照分组',
    providerCode: 'openai'
  }, access)
  repositories.setAccountGroup(accountA.id, group.id, access)
  repositories.setAccountGroup(accountB.id, group.id, access)

  serverConcurrency = {
    [accountA.id]: 2,
    [accountB.id]: 1
  }

  const fakeParent = new FakeServerParent()
  const previousProcessRole = runtimeConfig.processRole
  const previousSend = process.send
  try {
    ;(process as typeof process & { send?: (message: unknown) => boolean }).send = fakeParent.send.bind(fakeParent)
    runtimeConfig.processRole = 'db-service'

    const dbServiceLocalAccountPage = repositories.listAccountsPage(access, { limit: 20 })
    assert.equal(findAccount(dbServiceLocalAccountPage.items, accountA.id).currentConcurrency, 0, 'DB service 本地并发快照应为空')

    const accountPage = repositories.listAccountsPage(access, { limit: 20 })
    const accountPageWithRuntime = await runtimeSnapshot.applyServerAccountConcurrencyToAccountList(accountPage)
    assert.equal(findAccount(accountPageWithRuntime.items, accountA.id).currentConcurrency, 2, '账户列表应合并 server 当前并发 A')
    assert.equal(findAccount(accountPageWithRuntime.items, accountA.id).currentConcurrencyAvailable, true, '账户列表应标记 server 并发快照可用')
    assert.equal(findAccount(accountPageWithRuntime.items, accountB.id).currentConcurrency, 1, '账户列表应合并 server 当前并发 B')
    assert.equal(accountPageWithRuntime.runtimeSnapshot.accountConcurrencyAvailable, true, '账户分页结果应标记 server 并发快照可用')

    const groups = await runtimeSnapshot.applyServerAccountConcurrencyToGroups(repositories.listGroups(access))
    const targetGroup = groups.find((item) => item.id === group.id)
    assert(targetGroup, '测试分组应存在')
    assert.equal(targetGroup.accountStats.currentConcurrency, 3, '分组列表应汇总 server 当前并发')
    assert.equal(targetGroup.accountStats.currentConcurrencyAvailable, true, '分组列表应标记 server 并发快照可用')
    assert.equal(requestedScopes.length, 2, '账户列表和分组列表应各请求一次 server 并发快照')
    assert(requestedScopes.every((scope) => scope === 'account_concurrency'), '系统 API 应只请求轻量并发快照')
  } finally {
    runtimeConfig.processRole = previousProcessRole
    ;(process as typeof process & { send?: (message: unknown) => boolean }).send = previousSend
    serverConcurrency = {}
    requestedScopes.length = 0
  }

  console.log('网关实时并发快照回归通过：系统 API 通过 server 快照合并账户和分组当前并发')
} finally {
  try {
    databaseModule.getDatabase().close()
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
