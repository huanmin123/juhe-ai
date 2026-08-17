import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const runtimeSnapshotSource = readFileSync(new URL('../../modules/gateway/runtime/runtime-snapshot.service.ts', import.meta.url), 'utf8')
assert(
  /loadRedisAccountConcurrencySnapshot\(\s*accountConcurrencySnapshotIds\(result\.items\)\s*\)/.test(runtimeSnapshotSource),
  'Redis runtime state 下账户列表当前并发必须按当前页账号的并发事实 ID 批量读取'
)
assert(runtimeSnapshotSource.includes('loadAccountCurrentConcurrencyByIdsAsync(accountIds)'), 'Redis runtime state 下列表并发事实来源必须走账号并发批量读取入口')
assert(runtimeSnapshotSource.includes('loadRedisAccountConcurrencySnapshot(accountIds)'), 'Redis runtime state 下分组列表当前并发必须从 Redis 批量读取账号并发')
assert(runtimeSnapshotSource.includes("runtimeConfig.runtimeStateDriver === 'redis'"), '列表运行态快照必须显式区分 Redis runtime state')
assert(runtimeSnapshotSource.includes('account.authorizationInstanceSourceAccountId || account.id'), '授权实例当前并发必须读取来源账号并发槽')
const accountListHydrationSource = readFileSync(new URL('../../modules/accounts/account-status-snapshot.service.ts', import.meta.url), 'utf8')
assert.match(accountListHydrationSource, /Promise\.all\(\[[\s\S]*loadAccountRuntimeAvailabilityByKeys\([\s\S]*loadAccountConcurrencyByIds\(/, '账户完整列表必须并行批量读取当前页运行态与并发')
assert.doesNotMatch(accountListHydrationSource, /for \([^)]*account[^)]*\)[\s\S]*loadAccountConcurrencyByIds\(/, '账户完整列表不得逐账户读取并发')

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
  runtimeSnapshot,
  groupStatusSnapshot
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/db-service/db-service-ipc.js'),
  import('../../modules/gateway/runtime/runtime-snapshot.service.js'),
  import('../../modules/groups/group-status-snapshot.service.js')
])

let serverConcurrency: Record<string, number | string> = {}
let serverRuntimeAvailability: ServerRuntimeAvailabilitySnapshot = {}
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
            accountConcurrency: serverConcurrency,
            accountRuntimeAvailability: serverRuntimeAvailability
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
    providerCode: 'gpt'
  }, granteeAccess)
  const group = repositories.createGroup({
    name: '实时并发快照分组',
    providerCode: 'gpt'
  }, access)
  const accountA = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
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
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
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
  for (const account of [accountA, accountB]) {
    assert.equal(repositories.projectAccountHealthFixtureSuccess(account.id, {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3,
      statusCode: 200
    }), true, `实时并发快照 fixture 应先激活账户：${account.id}`)
  }
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
    [accountA.id]: '6',
    [accountB.id]: '1',
    [authorizedAccountA.id]: '4'
  }
  assert(authorizedAccountA.accountAuthorizationId, '授权实例应包含账户授权 ID')
  const authorizedRuntimeKey = `${authorizedAccountA.id}:authorized:${grantee.id}:${granteeTargetGroup.id}:${authorizedAccountA.accountAuthorizationId}`
  serverRuntimeAvailability = {
    [accountA.id]: {
      status: 'precheck_pending',
      reason: 'mock account precheck pending',
      since: '2026-06-16T00:00:00.000Z',
      failureCount: 6,
      distinctClientIpCount: 2,
      distinctApiKeyCount: 3,
      precheckAttemptCount: 1
    },
    [authorizedRuntimeKey]: {
      status: 'precheck_pending',
      reason: 'mock authorized account precheck pending',
      since: '2026-06-16T00:00:00.000Z',
      failureCount: 6,
      distinctClientIpCount: 2,
      distinctApiKeyCount: 2,
      precheckAttemptCount: 1
    }
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
    const accountAWithRuntime = findAccount(accountPageWithRuntime.items, accountA.id)
    assert.equal(accountAWithRuntime.currentConcurrency, 6, '账户列表应合并 server 当前并发 A')
    assert.equal(accountAWithRuntime.currentConcurrencyAvailable, true, '账户列表应标记 server 并发快照可用')
    assert.equal(accountAWithRuntime.runtimeAvailability?.status, 'precheck_pending', '账户列表应合并 server 事前确认运行态')
    assert.equal(accountAWithRuntime.effectiveAvailability?.status, 'runtime_precheck_pending', '账户列表应把事前确认合成为实际不可用状态')
    assert.equal(accountAWithRuntime.effectiveAvailability?.label, '待探针确认', '账户列表应展示事前确认文案')
    assert.equal(findAccount(accountPageWithRuntime.items, accountB.id).currentConcurrency, 1, '账户列表应合并 server 当前并发 B')
    assert.equal(accountPageWithRuntime.runtimeSnapshot.accountConcurrencyAvailable, true, '账户分页结果应标记 server 并发快照可用')

    const granteeAccountPage = repositories.listAccountsPage({ systemAccountId: grantee.id, role: 'user' as const }, { page: 1, pageSize: 20 })
    const authorizedAccountPageWithRuntime = await runtimeSnapshot.applyServerAccountConcurrencyToAccountList(granteeAccountPage)
    const authorizedAccount = findAccount(authorizedAccountPageWithRuntime.items, authorizedAccountA.id)
    assert.equal(authorizedAccount.accessType, 'authorized', '被授权用户账户列表应返回授权账户视角')
    assert.equal(authorizedAccount.concurrencyLimit, 10, '授权账户应展示来源账号并发上限')
    assert.equal(authorizedAccount.currentConcurrency, 6, '授权实例账户应按来源账户 ID 合并 server 当前并发')
    assert.equal(authorizedAccount.currentConcurrencyAvailable, true, '授权账户应标记 server 并发快照可用')
    assert.equal(authorizedAccount.runtimeAvailability?.status, 'precheck_pending', '授权账户列表应按绑定维度合并 server 事前确认运行态')
    assert.equal(authorizedAccount.effectiveAvailability?.status, 'runtime_precheck_pending', '授权账户列表应把绑定维度事前确认合成为实际不可用状态')
    assert.equal(authorizedAccountPageWithRuntime.runtimeSnapshot.accountConcurrencyAvailable, true, '仅包含授权账户时也应标记 server 并发快照可用')

    const groups = await runtimeSnapshot.applyServerAccountConcurrencyToGroups(repositories.listGroups(access))
    const targetGroup = groups.find((item) => item.id === group.id)
    assert(targetGroup, '测试分组应存在')
    assert.equal(targetGroup.accountStats.currentConcurrency, 7, '分组列表应汇总 server 当前并发')
    assert.equal(targetGroup.accountStats.currentConcurrencyAvailable, true, '分组列表应标记 server 并发快照可用')

    const groupSnapshot = await groupStatusSnapshot.getGroupStatusSnapshot(access, [group.id])
    assert.equal('runtimeSnapshot' in groupSnapshot, true, '分组状态快照必须返回实时并发可用性')
    assert.equal(groupSnapshot.runtimeSnapshot.accountConcurrencyAvailable, true, '分组状态快照应标记 server 并发快照可用')
    assert.equal(groupSnapshot.items[0]?.currentConcurrency, 7, '分组状态快照必须按成员账户汇总 server 当前并发')

    const authorizedGroupSnapshot = await groupStatusSnapshot.getGroupStatusSnapshot(granteeAccess, [granteeTargetGroup.id])
    assert.equal(authorizedGroupSnapshot.runtimeSnapshot.accountConcurrencyAvailable, true)
    assert.equal(authorizedGroupSnapshot.items[0]?.currentConcurrency, 6, '授权实例所在分组必须按来源账户 ID 汇总并发')
    assert.equal(requestedScopes.length, 2, '账户与分组连续读取必须复用短 TTL server 运行态快照，避免同次刷新重复 IPC')
    assert.deepEqual(requestedScopes, ['account_runtime', 'account_concurrency'], '系统 API 应只读取一次账户运行态和一次并发轻量快照')
  } finally {
    runtimeConfig.processRole = previousProcessRole
    ;(process as typeof process & { send?: (message: unknown) => boolean }).send = previousSend
    serverConcurrency = {}
    serverRuntimeAvailability = {}
    requestedScopes.length = 0
  }

  console.log('网关实时快照回归通过：系统 API 通过 server 快照合并账户并发、账户运行态和分组当前并发')
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

type ServerRuntimeAvailabilitySnapshot = Record<string, {
  status: 'normal' | 'local_suppressed' | 'half_open' | 'precheck_pending' | 'precheck_failed'
  reason?: string
  since?: string
  until?: string
  failureCount?: number
  distinctClientIpCount?: number
  distinctApiKeyCount?: number
  precheckAttemptCount?: number
  localFailureCount?: number
}>
