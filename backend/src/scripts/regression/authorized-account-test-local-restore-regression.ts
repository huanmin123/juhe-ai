import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { submitAccountTestAndWait } from '../shared/account-test-task-client.js'
import { installWorkerParentIpcHarness } from '../shared/worker-parent-ipc-harness.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorized-account-test-local-restore-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'authorized-account-test-local-restore.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorized-account-test-local-restore-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const restoreWorkerParentIpc = installWorkerParentIpcHarness()

const [
  { accountsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  { flushAllUsageRecordQueue },
  { flushAllOperationLogQueue },
  { closeSqliteReadWorkerPool },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/__aisys__/api/accounts', requireAdmin, accountsRouter)

interface AccountView {
  id: string
  status: string
  schedulable: boolean
  cooldownUntil?: string
  lastErrorMessage?: string
  authorizationInstanceSourceAccountId?: string
  ownerSystemAccountId?: string
}

interface AccountTestResult {
  success: boolean
  statusCode?: number
  accountStatusChanged?: boolean
  accountStatus?: string
  message: string
}

let appServer: ReturnType<typeof app.listen> | undefined
let mockOpenAIServer: http.Server | undefined
let failureMessageVariant: 'first' | 'second' = 'first'

try {
  mockOpenAIServer = createMockOpenAIServer()
  mockOpenAIServer.listen(0, '127.0.0.1')
  await onceListening(mockOpenAIServer)
  const mockAddress = mockOpenAIServer.address()
  if (!mockAddress || typeof mockAddress === 'string') {
    throw new Error('授权账户测试本地状态恢复 mock 上游地址不可用')
  }
  const mockBaseUrl = `http://127.0.0.1:${mockAddress.port}`

  appServer = app.listen(0, '127.0.0.1')
  await onceListening(appServer)
  const appAddress = appServer.address()
  if (!appAddress || typeof appAddress === 'string') {
    throw new Error('授权账户测试本地状态恢复服务地址不可用')
  }
  const appBaseUrl = `http://127.0.0.1:${appAddress.port}`

  const owner = repositories.createSystemAccount({
    username: 'test_local_restore_owner',
    displayName: '授权测试本地恢复所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'test_local_restore_grantee',
    displayName: '授权测试本地恢复被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const admin = repositories.createSystemAccount({
    username: 'test_local_restore_admin',
    displayName: '授权测试本地恢复管理员',
    password: 'password',
    role: 'admin',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeGroup = repositories.createGroup({
    name: '授权测试本地恢复分组',
    providerCode: 'gpt'
  }, granteeAccess)
  const ownerSourceGroup = repositories.createGroup({
    name: '授权测试本地恢复来源分组',
    providerCode: 'gpt'
  }, ownerAccess)
  const ownerAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '授权测试本地恢复账户',
    type: 'api_key',
    status: 'active',
    credentials: { api_key: 'sk-authorized-local-restore', base_url: mockBaseUrl },
    groupId: ownerSourceGroup.id
  }, ownerAccess)
  repositories.recordAccountHealthCheckSuccess(ownerAccount.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: ownerAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '授权账户测试本地恢复回归'
  }, ownerAccess)
  const granteeAccount = authorizedInstanceForSource(ownerAccount.id, granteeAccess)
  assert(repositories.setAccountGroup(granteeAccount.id, granteeGroup.id, granteeAccess), '授权实例账户绑定到被授权用户分组失败')
  repositories.recordAccountHealthCheckSuccess(granteeAccount.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  const activeGranteeAccount = authorizedInstanceForSource(ownerAccount.id, granteeAccess)
  const cooled = repositories.markAccountTestTemporaryUnavailable(activeGranteeAccount, '模拟授权账户测试失败', granteeAccess)
  assert.equal(cooled?.status, 'temporary_unavailable', '授权账户测试失败应写入被授权实例临时不可调用')
  assert.equal(repositories.findAccountSummary(ownerAccount.id, ownerAccess)?.status, 'active', '实例临时不可调用不应改变所有者原账户')

  const expiredLocalCooldownUntil = new Date(Date.now() - 1000).toISOString()
  databaseModule.getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET cooldown_until = ?,
          updated_at = ?
      WHERE id = ?
        AND system_account_id = ?
    `)
    .run(expiredLocalCooldownUntil, expiredLocalCooldownUntil, granteeAccount.id, grantee.id)
  const expiredCooledView = repositories.findAccountSummary(granteeAccount.id, granteeAccess) as AccountView | undefined
  assert.equal(expiredCooledView?.status, 'temporary_unavailable', '授权实例冷却到期后仍应显示临时不可调用，直到测试成功或手动恢复')
  assert.equal(expiredCooledView?.schedulable, false, '授权实例冷却到期但未恢复前不应重新进入调度')
  assert.equal(
    repositories.listAccounts(granteeAccess, { status: 'active' }).some((account) => account.id === granteeAccount.id),
    false,
    '授权实例失败态冷却到期后不应命中正常状态筛选'
  )
  assert(
    repositories.listAccounts(granteeAccess, { status: 'temporary_unavailable' }).some((account) => account.id === granteeAccount.id),
    '授权实例失败态冷却到期后应命中临时不可调用筛选'
  )
  assert(
    repositories.listAccounts(granteeAccess, { schedulable: 'cooling' }).some((account) => account.id === granteeAccount.id),
    '授权实例失败态冷却到期后应命中冷却筛选'
  )
  assert.equal(
    repositories.findOpenAIAccountForGroup(granteeGroup.id, granteeAccount.id, grantee.id),
    undefined,
    '授权实例失败态冷却到期后网关调度仍不应选中'
  )
  assert(
    repositories.findOpenAIAccountForGroup(granteeGroup.id, granteeAccount.id, grantee.id, { ignoreAvailability: true }),
    '授权实例失败态仍应允许测试链路按单账号诊断'
  )

  const result = await withWorkerRole(() => submitAccountTestAndWait<AccountTestResult>({
    baseUrl: appBaseUrl,
    path: `/__aisys__/api/my-accounts/${granteeAccount.id}/test`,
    cookie: sessionCookie(grantee.id),
    body: { model: 'gpt-5.5', testEndpointMode: 'responses_sse' }
  }))
  assert.equal(result.success, true, `授权实例临时不可调用时手动测试应允许探活：${result.message}`)
  assert.equal(result.statusCode, 200, '授权实例探活成功应返回上游状态码')
  assert.equal(result.accountStatusChanged, false, '授权实例探活成功不应在测试结果中标记状态变化')
  assert.equal(result.accountStatus, 'temporary_unavailable', '授权实例探活成功后结果应保留原状态')

  const granteeView = repositories.findAccountSummary(granteeAccount.id, granteeAccess) as AccountView | undefined
  assert.equal(granteeView?.status, 'temporary_unavailable', '测试成功后被授权用户视角应保留原状态')
  assert.equal(granteeView?.cooldownUntil, expiredLocalCooldownUntil, '测试成功后不应清理授权实例冷却时间')
  assert(granteeView?.lastErrorMessage, '测试成功后不应清理授权实例错误信息')
  assert.equal(repositories.findAccountSummary(ownerAccount.id, ownerAccess)?.status, 'active', '授权实例测试不应修改所有者原账户')

  const failingOwnerAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '授权测试本地失败账户',
    type: 'api_key',
    status: 'active',
    credentials: { api_key: 'sk-authorized-local-failure', base_url: mockBaseUrl },
    groupId: ownerSourceGroup.id
  }, ownerAccess)
  activateAccountAfterBackgroundCheck(failingOwnerAccount.id)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: failingOwnerAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '授权账户测试本地失败回归'
  }, ownerAccess)
  const failingGranteeAccount = authorizedInstanceForSource(failingOwnerAccount.id, granteeAccess)
  assert(repositories.setAccountGroup(failingGranteeAccount.id, granteeGroup.id, granteeAccess), '失败授权实例账户绑定到被授权用户分组失败')
  activateAccountAfterBackgroundCheck(failingGranteeAccount.id)

  const failureResult = await withWorkerRole(() => submitAccountTestAndWait<AccountTestResult>({
    baseUrl: appBaseUrl,
    path: `/__aisys__/api/accounts/${failingGranteeAccount.id}/test?systemAccountId=${encodeURIComponent(grantee.id)}`,
    cookie: sessionCookie(admin.id),
    body: { model: 'gpt-5.5', testEndpointMode: 'responses_sse' }
  }))
  assert.equal(failureResult.success, false, '授权账户测试收到上游失败时测试结果不应成功')
  assert.equal(failureResult.statusCode, 400, '授权账户测试应保留上游失败状态码用于诊断')
  assert.equal(failureResult.accountStatusChanged, false, '授权账户测试失败不应在主测试结果中直接改实例状态')
  assert.equal(failureResult.accountStatus, 'active', '授权账户测试失败应先保持当前实例状态，等待事前确认')
  assertNoLeak(JSON.stringify(failureResult), [
    'account-test-bearer-token',
    'sk-account-test-secret-token',
    'account-test-client-secret',
    'account-test-url-user',
    'account-test-url-password'
  ], '账户测试失败响应不应暴露上游错误体中的敏感串')

  const failedGranteeView = repositories.findAccountSummary(failingGranteeAccount.id, granteeAccess) as AccountView | undefined
  assert.equal(failedGranteeView?.status, 'active', '授权账户人工测试失败后应保持正常状态')
  assert.equal(failedGranteeView?.cooldownUntil, undefined, '授权账户人工测试失败后不应写入冷却时间')
  assert.equal(failedGranteeView?.lastErrorMessage, undefined, '授权账户人工测试失败后不应写入最近错误')
  assertNoLeak(failedGranteeView?.lastErrorMessage ?? '', [
    'account-test-bearer-token',
    'sk-account-test-secret-token',
    'account-test-client-secret',
    'account-test-url-user',
    'account-test-url-password'
  ], '账户测试失败后账户状态字段不应出现上游敏感串')

  failureMessageVariant = 'second'
  const secondFailureResult = await withWorkerRole(() => submitAccountTestAndWait<AccountTestResult>({
    baseUrl: appBaseUrl,
    path: `/__aisys__/api/accounts/${failingGranteeAccount.id}/test?systemAccountId=${encodeURIComponent(grantee.id)}`,
    cookie: sessionCookie(admin.id),
    body: { model: 'gpt-5.5', testEndpointMode: 'responses_sse' }
  }))
  assert.equal(secondFailureResult.success, false, '再次测试失败时结果仍不应成功')
  assert.equal(secondFailureResult.accountStatusChanged, false, '再次测试失败也不应写账户状态')
  const secondFailedView = repositories.findAccountSummary(failingGranteeAccount.id, granteeAccess) as AccountView | undefined
  assert.equal(secondFailedView?.status, 'active', '再次测试失败后授权实例仍应保持正常状态')
  assert.equal(secondFailedView?.lastErrorMessage, undefined, '再次测试失败后也不应覆盖最近错误')
  assertNoLeak(JSON.stringify(secondFailureResult), [
    'second-account-test-bearer-token',
    'sk-second-account-test-secret-token',
    'second-account-test-client-secret',
    'second-account-test-url-user',
    'second-account-test-url-password'
  ], '账户测试再次失败响应不应暴露上游敏感串')
  assertNoLeak(secondFailedView?.lastErrorMessage ?? '', [
    'second-account-test-bearer-token',
    'sk-second-account-test-secret-token',
    'second-account-test-client-secret',
    'second-account-test-url-user',
    'second-account-test-url-password'
  ], '账户测试再次失败写入最近错误前应清理上游敏感串')
  assert.equal(repositories.findAccountSummary(failingOwnerAccount.id, ownerAccess)?.status, 'active', '测试失败不应修改所有者原账户')

  const errorOwnerAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '授权测试本地异常账户',
    type: 'api_key',
    status: 'active',
    credentials: { api_key: 'sk-authorized-local-error-success', base_url: mockBaseUrl },
    groupId: ownerSourceGroup.id
  }, ownerAccess)
  activateAccountAfterBackgroundCheck(errorOwnerAccount.id)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: errorOwnerAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '授权账户异常测试入口回归'
  }, ownerAccess)
  const errorGranteeAccount = authorizedInstanceForSource(errorOwnerAccount.id, granteeAccess)
  assert(repositories.setAccountGroup(errorGranteeAccount.id, granteeGroup.id, granteeAccess), '异常授权实例账户绑定到被授权用户分组失败')
  activateAccountAfterBackgroundCheck(errorGranteeAccount.id)
  assert(errorGranteeAccount.accountAuthorizationId, '异常授权实例应带有稳定授权 ID')
  const disabledByFailure = repositories.markAuthorizedAccountBindingDisabledByFailure({
    accountId: errorGranteeAccount.id,
    systemAccountId: grantee.id,
    groupId: granteeGroup.id,
    accountAuthorizationId: errorGranteeAccount.accountAuthorizationId,
    reason: '模拟授权实例异常'
  })
  assert.equal(disabledByFailure?.status, 'error', '授权实例应被标记为本地异常')
  const errorView = repositories.findAccountSummary(errorGranteeAccount.id, granteeAccess)
  assert(errorView, '被授权用户视角应能读取异常授权实例')
  assert.equal(errorView.status, 'error', '被授权用户视角应看到授权实例异常')
  assert.equal(repositories.accountTestUnavailableMessage(errorView), undefined, '授权实例异常时仍应允许手动测试诊断')

  const errorRecoveryResult = await withWorkerRole(() => submitAccountTestAndWait<AccountTestResult>({
    baseUrl: appBaseUrl,
    path: `/__aisys__/api/my-accounts/${errorGranteeAccount.id}/test`,
    cookie: sessionCookie(grantee.id),
    body: { model: 'gpt-5.5', testEndpointMode: 'responses_sse' }
  }))
  assert.equal(errorRecoveryResult.success, true, `授权实例异常时手动测试应允许进入探活：${errorRecoveryResult.message}`)
  assert.equal(errorRecoveryResult.statusCode, 200, '授权实例异常探活成功应返回上游状态码')
  assert.equal(errorRecoveryResult.accountStatusChanged, false, '授权实例异常探活成功不应返回状态变化')
  assert.equal(errorRecoveryResult.accountStatus, 'error', '授权实例异常探活成功后应保留异常状态')
  assert.equal(repositories.findAccountSummary(errorGranteeAccount.id, granteeAccess)?.status, 'error', '授权实例异常探活成功后不应自动恢复')
  assert.equal(repositories.findAccountSummary(errorOwnerAccount.id, ownerAccess)?.status, 'active', '授权实例测试不应修改所有者原账户')

  console.log('授权账户人工测试状态隔离回归通过')
} finally {
  await closeServer(appServer)
  await closeServer(mockOpenAIServer)
  try {
    flushAllUsageRecordQueue()
    flushAllOperationLogQueue()
    await closeSqliteReadWorkerPool()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  restoreWorkerParentIpc()
  rmSync(tempRoot, { recursive: true, force: true })
}

function createMockOpenAIServer(): http.Server {
  return http.createServer((req, res) => {
    const requestPath = req.url?.split('?', 1)[0]
    if (req.method !== 'POST' || (requestPath !== '/v1/responses' && requestPath !== '/v1/chat/completions')) {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'not found' } }))
      return
    }
    req.on('end', () => {
      if (req.headers.authorization?.includes('sk-authorized-local-failure')) {
        res.writeHead(400, { 'content-type': 'application/json' })
        res.end(JSON.stringify({
          error: {
            code: 'key_switch_cooldown',
            message: failureMessageVariant === 'first'
              ? '切换key需要冷却30秒 Authorization: Bearer account-test-bearer-token sk-account-test-secret-token client_secret=account-test-client-secret url=https://account-test-url-user:account-test-url-password@example.com/v1'
              : '第二次测试失败仍需保持最新错误 Authorization: Bearer second-account-test-bearer-token sk-second-account-test-secret-token client_secret=second-account-test-client-secret url=https://second-account-test-url-user:second-account-test-url-password@example.com/v1',
            type: 'invalid_request_error'
          }
        }))
        return
      }
      if (requestPath === '/v1/chat/completions') {
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({
          id: 'chatcmpl_authorized_local_restore',
          object: 'chat.completion',
          choices: [{
            index: 0,
            message: { role: 'assistant', content: 'OK' },
            finish_reason: 'stop'
          }],
          usage: {
            prompt_tokens: 1,
            completion_tokens: 1,
            total_tokens: 2
          }
        }))
        return
      }
      const completedEvent = {
        type: 'response.completed',
        response: {
          id: 'resp_authorized_local_restore',
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
      res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
      res.end(`event: response.completed\ndata: ${JSON.stringify(completedEvent)}\n\n`)
    })
    req.resume()
  })
}

function authorizedInstanceForSource(sourceAccountId: string, access: { systemAccountId: string; role: 'user' }) {
  const account = repositories.listAccounts(access)
    .find((item) => item.authorizationInstanceSourceAccountId === sourceAccountId)
  assert(account, `被授权用户视角应能读取来源账户 ${sourceAccountId} 的授权实例`)
  return account
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

function activateAccountAfterBackgroundCheck(accountId: string): void {
  const changed = repositories.recordAccountHealthCheckSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  })
  assert.equal(changed, true, `后台健康检查激活账户失败：${accountId}`)
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


function assertNoLeak(text: string, markers: string[], message: string): void {
  for (const marker of markers) {
    assert(!text.includes(marker), `${message}：${marker}`)
  }
}
