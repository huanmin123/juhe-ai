import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { submitAccountTestAndWait } from '../shared/account-test-task-client.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorized-account-admin-dispatch-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'authorized-account-admin-dispatch.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorized-account-admin-dispatch-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { accountsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  { testOpenAIAccount },
  { orderOpenAIAccountsBySessionAffinity },
  { flushAllUsageRecordQueue },
  { flushAllOperationLogQueue },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../modules/accounts/account-test.service.js'),
  import('../../modules/gateway/runtime/session-affinity.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/__aisys__/api/accounts', requireAdmin, accountsRouter)

const accountsRoutesSource = readFileSync(new URL('../../modules/accounts/accounts.routes.ts', import.meta.url), 'utf8')
const accountAuthorizedDispatchRoutesSource = readFileSync(new URL('../../modules/accounts/account-authorized-dispatch.routes.ts', import.meta.url), 'utf8')
const accountTrafficMigrationRoutesSource = readFileSync(new URL('../../modules/accounts/account-traffic-migration.routes.ts', import.meta.url), 'utf8')
assert(
  accountsRoutesSource.includes('registerAccountAuthorizedDispatchRoutes(accountsRouter)'),
  '账户主路由必须通过 registerAccountAuthorizedDispatchRoutes 注册授权调度路由'
)
assert(
  !accountsRoutesSource.includes("accountsRouter.patch('/:id/authorized-dispatch'"),
  '账户主路由不应直接承载授权调度路由实现'
)
assert(
  accountAuthorizedDispatchRoutesSource.includes("operationKey: 'accounts.authorized_dispatch'"),
  '账户授权调度子路由必须保留操作日志 operationKey'
)
assert(
  accountAuthorizedDispatchRoutesSource.includes('clearServerAccountRuntimeAvailability'),
  '账户授权调度子路由必须保留恢复调度时的网关运行态清理逻辑'
)
assert(
  accountsRoutesSource.includes('registerAccountTrafficMigrationRoutes(accountsRouter)'),
  '账户主路由必须通过 registerAccountTrafficMigrationRoutes 注册流量迁移路由'
)
assert(
  !accountsRoutesSource.includes("accountsRouter.post('/:id/traffic-migration'"),
  '账户主路由不应直接承载流量迁移路由实现'
)
assert(
  accountTrafficMigrationRoutesSource.includes("operationKey: 'accounts.traffic_migration'"),
  '账户流量迁移子路由必须保留操作日志 operationKey'
)
assert(
  accountTrafficMigrationRoutesSource.includes('migrateServerOpenAIAccountTrafficRuntime'),
  '账户流量迁移子路由必须通过网关 server 运行态迁移 OpenAI 会话亲和'
)
assert(
  accountTrafficMigrationRoutesSource.includes('migrateServerOpenAIAccountTrafficRuntime'),
  '账户流量迁移子路由必须把会话亲和迁移和目标偏向投递到网关 server 运行态'
)

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface AccountSummary {
  id: string
  name: string
  status: string
  effectiveAvailability?: {
    available: boolean
    status: string
    label: string
    reason?: string
  }
  accessType?: string
  boundGroupId?: string
  bindingSystemAccountId?: string
  ownerSystemAccountId?: string
  authorizationInstanceSourceAccountId?: string
  schedulable?: boolean
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
}

interface AccountListResult {
  items: AccountSummary[]
  total: number
  page: number
  pageSize: number
}

interface AccountTestResult {
  success: boolean
  message: string
  statusCode?: number
  outputText?: string
  requestUrl?: string
  requestBody?: unknown
  responseHeaders?: unknown
  responseBody?: unknown
  responseText?: string
  modelsUrl?: string
  proxyUrl?: string
  tokenRefreshed?: boolean
}

interface AccountTrafficMigrationResult {
  sourceAccount: AccountSummary
  targetAccount: AccountSummary
  migratedSessionCount: number
  sourceStatus: string
}

interface SeedState {
  adminCookie: string
  granteeCookie: string
  ownerAccountId: string
  ownerSourceAccountId: string
  ownerErrorAccountId: string
  ownerErrorSourceAccountId: string
  ownerPausedAccountId: string
  ownerPausedSourceAccountId: string
  ownerId: string
  granteeGroupId: string
  granteeTargetAccountId: string
  granteeId: string
}

let server: ReturnType<typeof app.listen> | undefined
let mockOpenAIServer: http.Server | undefined

try {
  mockOpenAIServer = createMockOpenAIServer()
  mockOpenAIServer.listen(0, '127.0.0.1')
  await onceListening(mockOpenAIServer)
  const mockAddress = mockOpenAIServer.address()
  if (!mockAddress || typeof mockAddress === 'string') {
    throw new Error('管理员授权账户调度 mock 上游地址不可用')
  }
  const mockBaseUrl = `http://127.0.0.1:${mockAddress.port}`

  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('管理员授权账户调度回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`
  const seed = seedData(mockBaseUrl)

  const adminGranteeAccounts = await getEnvelope<AccountListResult>(
    baseUrl,
    `/__aisys__/api/accounts?systemAccountId=${seed.granteeId}&page=1&pageSize=20`,
    seed.adminCookie
  )
  const authorizedAccount = adminGranteeAccounts.items.find((account) => account.id === seed.ownerAccountId)
  assert(authorizedAccount, '管理员按用户筛选时应能看到该用户可用的授权账户')
  assert.equal(authorizedAccount.accessType, 'authorized', '授权账户在被授权用户作用域下应保持 authorized 视角')
  assert.equal(authorizedAccount.boundGroupId, seed.granteeGroupId, '授权账户应返回被授权用户自己的绑定分组')
  assert.equal(authorizedAccount.bindingSystemAccountId, seed.granteeId, '授权账户应返回本地绑定所属系统账户，供管理侧代操作写入同一作用域')

  const locallyDisabled = await patchEnvelope<AccountSummary>(
    baseUrl,
    `/__aisys__/api/my-accounts/${seed.ownerAccountId}/authorized-dispatch`,
    seed.granteeCookie,
    { status: 'disabled' }
  )
  assert.equal(locallyDisabled.status, 'disabled', '被授权用户应能在自己的分组内停用授权账户')
  assert.equal(repositories.listAccounts({ systemAccountId: seed.ownerId, role: 'user' as const }).find((account) => account.id === seed.ownerSourceAccountId)?.status, 'active', '停用授权实例不应修改账户所有者原账户状态')
  const granteeDisabledAccounts = await getEnvelope<AccountListResult>(
    baseUrl,
    '/__aisys__/api/my-accounts?page=1&pageSize=20',
    seed.granteeCookie
  )
  const granteeDisabledAccount = granteeDisabledAccounts.items.find((account) => account.id === seed.ownerAccountId)
  assert.equal(granteeDisabledAccount?.status, 'disabled', '被授权用户停用后重新拉取列表应显示停用')
  assert.equal(granteeDisabledAccount?.schedulable, false, '本地停用后授权账户在被授权用户视角不可调度')
  const adminGranteeDisabledAccounts = await getEnvelope<AccountListResult>(
    baseUrl,
    `/__aisys__/api/accounts?systemAccountId=${seed.granteeId}&page=1&pageSize=20`,
    seed.adminCookie
  )
  const adminGranteeDisabledAccount = adminGranteeDisabledAccounts.items.find((account) => account.id === seed.ownerAccountId)
  assert.equal(adminGranteeDisabledAccount?.status, 'disabled', '管理员查看被授权用户作用域时应显示授权账户本地停用')
  const adminGranteeDisabledStatusFilteredAccounts = await getEnvelope<AccountListResult>(
    baseUrl,
    `/__aisys__/api/accounts?systemAccountId=${seed.granteeId}&page=1&pageSize=20&status=disabled`,
    seed.adminCookie
  )
  assert.equal(adminGranteeDisabledStatusFilteredAccounts.items.some((account) => account.id === seed.ownerAccountId), true, '管理员按停用状态筛选被授权用户作用域时应能看到本地停用的授权账户')
  const granteeDisabledStatusFilteredAccounts = await getEnvelope<AccountListResult>(
    baseUrl,
    '/__aisys__/api/my-accounts?page=1&pageSize=20&status=disabled',
    seed.granteeCookie
  )
  assert.equal(granteeDisabledStatusFilteredAccounts.items.some((account) => account.id === seed.ownerAccountId), true, '被授权用户按停用状态筛选时应能看到本地停用的授权账户')
  const granteeActiveStatusFilteredAccounts = await getEnvelope<AccountListResult>(
    baseUrl,
    '/__aisys__/api/my-accounts?page=1&pageSize=20&status=active',
    seed.granteeCookie
  )
  assert.equal(granteeActiveStatusFilteredAccounts.items.some((account) => account.id === seed.ownerAccountId), false, '被授权用户按正常状态筛选时不应继续看到本地停用的授权账户')
  const granteeSchedulableEnabledAccounts = await getEnvelope<AccountListResult>(
    baseUrl,
    '/__aisys__/api/my-accounts?page=1&pageSize=20&schedulable=enabled',
    seed.granteeCookie
  )
  assert.equal(granteeSchedulableEnabledAccounts.items.some((account) => account.id === seed.ownerAccountId), false, '被授权用户按可调度筛选时不应看到本地停用的授权账户')
  const granteeSchedulableDisabledAccounts = await getEnvelope<AccountListResult>(
    baseUrl,
    '/__aisys__/api/my-accounts?page=1&pageSize=20&schedulable=disabled',
    seed.granteeCookie
  )
  assert.equal(granteeSchedulableDisabledAccounts.items.some((account) => account.id === seed.ownerAccountId), true, '被授权用户按不可调度筛选时应看到本地停用的授权账户')
  const granteeDisabledOptions = await getEnvelope<AccountSummary[]>(
    baseUrl,
    '/__aisys__/api/my-accounts/options?status=disabled&limit=20',
    seed.granteeCookie
  )
  assert.equal(granteeDisabledOptions.some((account) => account.id === seed.ownerAccountId && account.status === 'disabled'), true, '账户选项应按授权实例状态筛选和展示')
  const ownerAfterGranteeDisabledAccounts = await getEnvelope<AccountListResult>(
    baseUrl,
    `/__aisys__/api/accounts?systemAccountId=${seed.ownerId}&page=1&pageSize=20`,
    seed.adminCookie
  )
  assert.equal(ownerAfterGranteeDisabledAccounts.items.find((account) => account.id === seed.ownerSourceAccountId)?.status, 'active', '授权实例停用不应影响所有者列表状态')
  const locallyEnabled = await patchEnvelope<AccountSummary>(
    baseUrl,
    `/__aisys__/api/my-accounts/${seed.ownerAccountId}/authorized-dispatch`,
    seed.granteeCookie,
    { status: 'active' }
  )
  assert.equal(locallyEnabled.status, 'active', '被授权用户应能重新启用自己的授权账户绑定')

  const updated = await patchEnvelope<AccountSummary>(
    baseUrl,
    `/__aisys__/api/accounts/${seed.ownerAccountId}/authorized-dispatch?systemAccountId=${authorizedAccount.bindingSystemAccountId}`,
    seed.adminCookie,
    { superPriorityEnabled: true }
  )
  assert.equal(updated.accessType, 'authorized', '管理员代操作后仍应返回授权账户视角')
  assert.equal(updated.bindingSystemAccountId, seed.granteeId, '管理员代操作响应应保留被代操作用户作用域')
  assert.equal(updated.superPriorityEnabled, true, '管理员应能代被授权用户开启本地超级优先')

  const granteeView = repositories.listAccounts({ systemAccountId: seed.granteeId, role: 'user' as const })
    .find((account) => account.id === seed.ownerAccountId)
  const ownerView = repositories.listAccounts({ systemAccountId: seed.ownerId, role: 'user' as const })
    .find((account) => account.id === seed.ownerSourceAccountId)
  assert.equal(granteeView?.superPriorityEnabled, true, '管理员代操作应写入被授权用户自己的本地调度标记')
  assert.equal(ownerView?.superPriorityEnabled, false, '管理员代操作授权账户不应修改账户所有者原始超级优先')

  const testAccount = repositories.findAccountForTest(seed.ownerAccountId, { systemAccountId: seed.granteeId, role: 'user' as const })
  assert.equal(testAccount?.accessType, 'authorized', '被授权用户视角应能拿到授权账户测试对象')
  assert.equal(testAccount?.bindingSystemAccountId, seed.granteeId, '测试对象应保留本地绑定所属系统账户')
  const tested: AccountTestResult = await withDbServiceRole(() => testOpenAIAccount(testAccount, { model: 'gpt-5.5', prompt: 'hi' }))
  assert.equal(tested.success, true, `管理员应能代被授权用户测试授权账户：${tested.message}`)
  assert.equal(tested.statusCode, 200, '授权账户测试应通过被授权用户自己的分组绑定进入网关链路')

  const granteeLimitedTest = await withWorkerRole(() => submitAccountTestAndWait<AccountTestResult>({
    baseUrl,
    path: `/__aisys__/api/my-accounts/${seed.ownerAccountId}/test`,
    cookie: seed.granteeCookie,
    body: { model: 'gpt-5.5', prompt: 'hi' }
  }))
  assert.equal(granteeLimitedTest.success, true, `被授权用户应能测试已绑定且可用的授权账户：${granteeLimitedTest.message}`)
  assert.equal(granteeLimitedTest.statusCode, 200, '被授权用户测试应保留状态码')
  assert.equal(granteeLimitedTest.outputText, 'OK', '被授权用户测试成功时可看到模型输出')
  assert.equal(granteeLimitedTest.requestUrl, undefined, '被授权用户测试结果不应暴露请求 URL 诊断')
  assert.equal(granteeLimitedTest.requestBody, undefined, '被授权用户测试结果不应暴露请求体诊断')
  assert.equal(granteeLimitedTest.responseHeaders, undefined, '被授权用户测试结果不应暴露上游响应头')
  assert.equal(granteeLimitedTest.responseBody, undefined, '被授权用户测试结果不应暴露上游响应体')
  assert.equal(granteeLimitedTest.responseText, undefined, '被授权用户测试成功时不应暴露原始响应文本')
  assert.equal(granteeLimitedTest.modelsUrl, undefined, '被授权用户测试结果不应暴露模型 URL 诊断')
  assert.equal(granteeLimitedTest.proxyUrl, undefined, '被授权用户测试结果不应暴露代理诊断')
  assert.equal(granteeLimitedTest.tokenRefreshed, undefined, '被授权用户测试结果不应暴露所有者 token 刷新诊断')

  const ownerPausedByOwner = repositories.updateAccount(seed.ownerPausedSourceAccountId, { status: 'disabled' }, { systemAccountId: seed.ownerId, role: 'user' as const })
  assert.equal(ownerPausedByOwner?.status, 'disabled', '归属人应能停用自己的主账户')
  assert.equal(ownerPausedByOwner?.schedulable, false, '归属人停用主账户后主账户自身不可调度')
  const ownerPausedAuthorizedAccounts = await getEnvelope<AccountListResult>(
    baseUrl,
    '/__aisys__/api/my-accounts?page=1&pageSize=20',
    seed.granteeCookie
  )
  const ownerPausedAuthorizedAccount = ownerPausedAuthorizedAccounts.items.find((account) => account.id === seed.ownerPausedAccountId)
  assert.equal(ownerPausedAuthorizedAccount?.status, 'active', '归属人停用主账户后授权实例仍应保持自己的状态')
  assert.equal(ownerPausedAuthorizedAccount?.schedulable, false, '归属人停用主账户后授权实例实际不可调度')
  assert.equal(ownerPausedAuthorizedAccount?.effectiveAvailability?.status, 'source_disabled', '归属人停用主账户后授权实例实际状态应标记来源停用')
  const ownerPausedEnabledAccounts = await getEnvelope<AccountListResult>(
    baseUrl,
    '/__aisys__/api/my-accounts?page=1&pageSize=20&schedulable=enabled',
    seed.granteeCookie
  )
  assert.equal(ownerPausedEnabledAccounts.items.some((account) => account.id === seed.ownerPausedAccountId), false, '归属人停用主账户后授权实例不应出现在可调度筛选中')
  const ownerPausedDisabledAccounts = await getEnvelope<AccountListResult>(
    baseUrl,
    '/__aisys__/api/my-accounts?page=1&pageSize=20&schedulable=disabled',
    seed.granteeCookie
  )
  assert.equal(ownerPausedDisabledAccounts.items.some((account) => account.id === seed.ownerPausedAccountId), true, '归属人停用主账户后授权实例应出现在不可调度筛选中')
  await assert.rejects(() => patchEnvelope<AccountSummary>(
    baseUrl,
    `/__aisys__/api/my-accounts/${seed.ownerPausedAccountId}/authorized-dispatch`,
    seed.granteeCookie,
    { fallbackEnabled: true }
  ), /授权方原账户已停用/, '归属人停用主账户后不应允许被授权用户开启备用标记')
  const ownerPausedGatewayAccounts = repositories.listOpenAIAccountsForGroup(seed.granteeGroupId, seed.granteeId)
  assert.equal(ownerPausedGatewayAccounts.some((account) => account.id === seed.ownerPausedAccountId && account.accountAccessType === 'account_authorized'), false, '网关调度应因归属人停用主账户排除授权实例')
  await assert.rejects(() => withDbServiceRole(() => postEnvelope<AccountTestResult>(
    baseUrl,
    `/__aisys__/api/my-accounts/${seed.ownerPausedAccountId}/test`,
    seed.granteeCookie,
    { model: 'gpt-5.5', prompt: 'hi' }
  )), /授权方原账户已停用/, '归属人停用主账户后被授权用户测试应被前置拦截')

  const granteeLimitedErrorTest = await withWorkerRole(() => submitAccountTestAndWait<AccountTestResult>({
    baseUrl,
    path: `/__aisys__/api/my-accounts/${seed.ownerErrorAccountId}/test`,
    cookie: seed.granteeCookie,
    body: { model: 'gpt-5.5-diagnostic-error', prompt: 'hi' }
  }))
  assert.equal(granteeLimitedErrorTest.success, false, '被授权用户测试上游错误时应返回测试失败')
  assert.equal(typeof granteeLimitedErrorTest.statusCode, 'number', '被授权用户测试上游错误时可保留 HTTP 状态码')
  assert.equal(granteeLimitedErrorTest.message, `账户测试未通过，上游返回 HTTP ${granteeLimitedErrorTest.statusCode}；请联系授权人或管理员查看完整诊断`)
  assert.equal(granteeLimitedErrorTest.responseText, granteeLimitedErrorTest.message, '被授权用户测试错误时只返回脱敏失败说明')
  assert.equal(granteeLimitedErrorTest.requestUrl, undefined, '被授权用户测试错误不应暴露请求 URL')
  assert.equal(granteeLimitedErrorTest.requestBody, undefined, '被授权用户测试错误不应暴露请求体')
  assert.equal(granteeLimitedErrorTest.responseHeaders, undefined, '被授权用户测试错误不应暴露上游响应头')
  assert.equal(granteeLimitedErrorTest.responseBody, undefined, '被授权用户测试错误不应暴露上游响应体')
  assert.equal(granteeLimitedErrorTest.modelsUrl, undefined, '被授权用户测试错误不应暴露模型 URL')
  assert.equal(granteeLimitedErrorTest.proxyUrl, undefined, '被授权用户测试错误不应暴露代理诊断')
  assert.equal(granteeLimitedErrorTest.tokenRefreshed, undefined, '被授权用户测试错误不应暴露 token 刷新诊断')
  assert(!JSON.stringify(granteeLimitedErrorTest).includes('owner-only'), '被授权用户测试错误不应泄露上游私有诊断内容')

  const migration = await postEnvelope<AccountTrafficMigrationResult>(
    baseUrl,
    `/__aisys__/api/accounts/${seed.ownerAccountId}/traffic-migration?systemAccountId=${authorizedAccount.bindingSystemAccountId}`,
    seed.adminCookie,
    { targetAccountId: seed.granteeTargetAccountId, sourceStatus: 'temporary_unavailable' }
  )
  assert.equal(migration.sourceAccount.status, 'temporary_unavailable', '管理员应能代被授权用户迁移授权账户流量')
  assert.equal(migration.sourceAccount.bindingSystemAccountId, seed.granteeId, '迁移响应应保留被授权用户本地绑定作用域')
  assert.equal(migration.targetAccount.id, seed.granteeTargetAccountId, '迁移目标应使用被授权用户分组内的可用账户')
  assert.equal(repositories.listAccounts({ systemAccountId: seed.ownerId, role: 'user' as const }).find((account) => account.id === seed.ownerSourceAccountId)?.status, 'active', '管理员迁移授权账户不应修改账户所有者原账户状态')
  assert.equal(repositories.listAccounts({ systemAccountId: seed.granteeId, role: 'user' as const }).find((account) => account.id === seed.ownerAccountId)?.status, 'temporary_unavailable', '管理员迁移授权账户应只写入被授权用户授权实例状态')
  const postMigrationCandidates = repositories.listOpenAIAccountsForGroup(seed.granteeGroupId, seed.granteeId)
  const postMigrationOrderedCandidates = orderOpenAIAccountsBySessionAffinity(postMigrationCandidates, undefined, {
    trafficMigrationScope: {
      systemAccountId: seed.granteeId,
      groupId: seed.granteeGroupId
    }
  })
  assert.equal(postMigrationOrderedCandidates[0]?.id, seed.granteeTargetAccountId, '迁移后同分组新请求应优先尝试迁移目标账户')

  console.log('管理员代操作授权账户调度回归通过')
} finally {
  await closeServer(server)
  await closeServer(mockOpenAIServer)
  try {
    flushAllUsageRecordQueue()
    flushAllOperationLogQueue()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  try {
    rmSync(tempRoot, { recursive: true, force: true })
  } catch {
  }
}

function createMockOpenAIServer(): http.Server {
  return http.createServer((req, res) => {
    if (req.method !== 'POST' || req.url?.split('?', 1)[0] !== '/v1/responses') {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'not found' } }))
      return
    }
    let requestBody = ''
    req.setEncoding('utf8')
    req.on('data', (chunk) => {
      requestBody += chunk
    })
    req.on('end', () => {
      if (requestBody.includes('gpt-5.5-diagnostic-error')) {
        res.writeHead(502, { 'content-type': 'application/json', 'x-upstream-diagnostic': 'owner-only-header' })
        res.end(JSON.stringify({ error: { message: 'owner-only upstream diagnostic body' } }))
        return
      }
      const completedEvent = {
        type: 'response.completed',
        response: {
          id: 'resp_authorized_admin_dispatch',
          object: 'response',
          status: 'completed',
          output: [
            {
              type: 'message',
              content: [{ type: 'output_text', text: 'OK' }]
            }
          ],
          usage: {
            input_tokens: 1,
            output_tokens: 1,
            total_tokens: 2
          }
        }
      }
      res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8', 'x-upstream-diagnostic': 'owner-only' })
      res.end(`event: response.completed\ndata: ${JSON.stringify(completedEvent)}\n\n`)
    })
  })
}

function seedData(mockBaseUrl: string): SeedState {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const owner = repositories.createSystemAccount({
    username: 'admin_dispatch_owner',
    displayName: '管理员调度所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'admin_dispatch_grantee',
    displayName: '管理员调度被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeDefaultGroup = repositories.listGroups(granteeAccess).find((group) => group.providerCode === 'gpt' && group.isDefault)
  assert(granteeDefaultGroup, '被授权用户默认 GPT 分组不存在')
  const ownerSourceGroup = repositories.createGroup({
    name: '管理员代操作授权来源分组',
    providerCode: 'gpt'
  }, ownerAccess)
  const ownerAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '管理员代操作授权账户',
    type: 'api_key',
    credentials: { api_key: 'sk-admin-authorized-dispatch', base_url: mockBaseUrl },
    groupId: ownerSourceGroup.id,
    status: 'active',
    schedulable: true
  }, ownerAccess)
  const ownerErrorAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '管理员代操作授权错误脱敏账户',
    type: 'api_key',
    credentials: { api_key: 'sk-admin-authorized-dispatch-error', base_url: mockBaseUrl },
    groupId: ownerSourceGroup.id,
    status: 'active',
    schedulable: true
  }, ownerAccess)
  const ownerPausedAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '管理员代操作归属人停用账户',
    type: 'api_key',
    credentials: { api_key: 'sk-admin-authorized-dispatch-owner-disabled', base_url: mockBaseUrl },
    groupId: ownerSourceGroup.id,
    status: 'active',
    schedulable: true
  }, ownerAccess)
  const granteeTargetAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '管理员代操作迁移目标账户',
    type: 'api_key',
    credentials: { api_key: 'sk-admin-authorized-dispatch-target', base_url: mockBaseUrl },
    groupId: granteeDefaultGroup.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, granteeAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: ownerAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeDefaultGroup.id,
    remark: '管理员代操作调度回归'
  }, ownerAccess)
  const defaultBoundAccount = authorizedInstanceForSource(ownerAccount.id, granteeAccess)
  assert.equal(defaultBoundAccount?.boundGroupId, granteeDefaultGroup.id, '授权账户生效后应默认绑定到被授权用户自己的默认分组')
  assert.equal(defaultBoundAccount?.bindingSystemAccountId, grantee.id, '授权账户默认绑定应归属被授权用户本地作用域')
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: ownerErrorAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeDefaultGroup.id,
    remark: '管理员代操作调度错误脱敏回归'
  }, ownerAccess)
  const ownerErrorAuthorizedAccount = authorizedInstanceForSource(ownerErrorAccount.id, granteeAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: ownerPausedAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeDefaultGroup.id,
    remark: '管理员代操作归属人停用隔离回归'
  }, ownerAccess)
  const ownerPausedAuthorizedAccount = authorizedInstanceForSource(ownerPausedAccount.id, granteeAccess)
  const granteeGroup = repositories.createGroup({
    name: '管理员代操作被授权分组',
    providerCode: 'gpt'
  }, granteeAccess)
  assert(repositories.setAccountGroup(defaultBoundAccount.id, granteeGroup.id, granteeAccess), '授权实例账户绑定到被授权用户分组失败')
  assert(repositories.setAccountGroup(ownerErrorAuthorizedAccount.id, granteeGroup.id, granteeAccess), '错误脱敏授权实例绑定到被授权用户分组失败')
  assert(repositories.setAccountGroup(ownerPausedAuthorizedAccount.id, granteeGroup.id, granteeAccess), '归属人停用隔离授权实例绑定到被授权用户分组失败')
  assert(repositories.setAccountGroup(granteeTargetAccount.id, granteeGroup.id, granteeAccess), '迁移目标账户绑定到被授权用户分组失败')
  return {
    adminCookie: sessionCookie(admin.id),
    granteeCookie: sessionCookie(grantee.id),
    ownerAccountId: defaultBoundAccount.id,
    ownerSourceAccountId: ownerAccount.id,
    ownerErrorAccountId: ownerErrorAuthorizedAccount.id,
    ownerErrorSourceAccountId: ownerErrorAccount.id,
    ownerPausedAccountId: ownerPausedAuthorizedAccount.id,
    ownerPausedSourceAccountId: ownerPausedAccount.id,
    ownerId: owner.id,
    granteeGroupId: granteeGroup.id,
    granteeTargetAccountId: granteeTargetAccount.id,
    granteeId: grantee.id
  }
}

function authorizedInstanceForSource(sourceAccountId: string, access: { systemAccountId: string; role: 'user' }): AccountSummary {
  const account = repositories.listAccounts(access)
    .find((item) => item.authorizationInstanceSourceAccountId === sourceAccountId) as AccountSummary | undefined
  assert(account, `被授权用户视角应能读取来源账户 ${sourceAccountId} 的授权实例`)
  return account
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  return parseEnvelope<T>(path, response)
}

async function patchEnvelope<T>(baseUrl: string, path: string, cookie: string, body: Record<string, unknown>): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  return parseEnvelope<T>(path, response)
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: Record<string, unknown>): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  return parseEnvelope<T>(path, response)
}

async function withDbServiceRole<T>(action: () => Promise<T>): Promise<T> {
  const previousProcessRole = runtimeConfig.processRole
  try {
    runtimeConfig.processRole = 'db-service'
    return await action()
  } finally {
    runtimeConfig.processRole = previousProcessRole
  }
}

async function withWorkerRole<T>(action: () => Promise<T>): Promise<T> {
  const previousProcessRole = runtimeConfig.processRole
  try {
    runtimeConfig.processRole = 'worker'
    return await action()
  } finally {
    runtimeConfig.processRole = previousProcessRole
  }
}

async function parseEnvelope<T>(path: string, response: Response): Promise<T> {
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function onceListening(listeningServer: ReturnType<typeof app.listen>): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: ReturnType<typeof app.listen>): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    const timeout = setTimeout(() => {
      listeningServer.closeAllConnections?.()
      resolvePromise()
    }, 1000)
    listeningServer.close((error) => {
      clearTimeout(timeout)
      if (error) {
        rejectPromise(error)
      } else {
        resolvePromise()
      }
    })
    listeningServer.closeIdleConnections?.()
  })
}
