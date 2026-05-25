import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorized-account-test-local-restore-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'authorized-account-test-local-restore.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorized-account-test-local-restore-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { accountsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  { flushAllUsageRecordQueue },
  { flushAllOperationLogQueue },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../modules/gateway/usage-record-queue.service.js'),
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

interface ApiEnvelope<T> {
  data: T
}

interface AccountView {
  id: string
  status: string
  localStatus?: string
  localCooldownUntil?: string
  localLastErrorMessage?: string
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
    providerCode: 'openai'
  }, granteeAccess)
  const ownerAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '授权测试本地恢复账户',
    type: 'api_key',
    credentials: { api_key: 'sk-authorized-local-restore', base_url: mockBaseUrl }
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: ownerAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: '授权账户测试本地恢复回归'
  }, ownerAccess)
  assert(repositories.setAccountGroup(ownerAccount.id, granteeGroup.id, granteeAccess), '授权账户绑定到被授权用户分组失败')

  const granteeAccount = repositories.findAccountSummary(ownerAccount.id, granteeAccess)
  assert(granteeAccount, '被授权用户视角应能读取授权账户')
  const cooled = repositories.markAccountTestTemporaryUnavailable(granteeAccount, '模拟授权账户测试失败', granteeAccess)
  assert.equal(cooled?.status, 'temporary_unavailable', '授权账户测试失败应写入被授权人本地临时不可调用')
  assert.equal(cooled?.localStatus, 'temporary_unavailable', '授权账户临时不可调用应保留本地状态')
  assert.equal(repositories.findAccountSummary(ownerAccount.id, ownerAccess)?.status, 'active', '本地临时不可调用不应改变所有者物理账户')

  const result = await postEnvelope<AccountTestResult>(
    appBaseUrl,
    `/__aisys__/api/my-accounts/${ownerAccount.id}/test`,
    sessionCookie(grantee.id),
    { model: 'gpt-5.5' }
  )
  assert.equal(result.success, true, `授权账户本地临时不可调用时手动测试应允许探活：${result.message}`)
  assert.equal(result.statusCode, 200, '授权账户探活成功应返回上游状态码')
  assert.equal(result.accountStatusChanged, true, '授权账户本地状态恢复应在测试结果中标记状态变化')
  assert.equal(result.accountStatus, 'active', '授权账户本地状态恢复后结果状态应为正常')

  const granteeView = repositories.findAccountSummary(ownerAccount.id, granteeAccess) as AccountView | undefined
  assert.equal(granteeView?.status, 'active', '测试成功后被授权用户视角应恢复正常')
  assert.equal(granteeView?.localStatus, 'active', '测试成功后应清理授权账户本地状态')
  assert.equal(granteeView?.localCooldownUntil, undefined, '测试成功后应清理授权账户本地冷却时间')
  assert.equal(granteeView?.localLastErrorMessage, undefined, '测试成功后应清理授权账户本地错误信息')
  assert.equal(repositories.findAccountSummary(ownerAccount.id, ownerAccess)?.status, 'active', '测试恢复授权本地状态不应修改所有者物理账户')

  const failingOwnerAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '授权测试本地失败账户',
    type: 'api_key',
    credentials: { api_key: 'sk-authorized-local-failure', base_url: mockBaseUrl }
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: failingOwnerAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: '授权账户测试本地失败回归'
  }, ownerAccess)
  assert(repositories.setAccountGroup(failingOwnerAccount.id, granteeGroup.id, granteeAccess), '失败授权账户绑定到被授权用户分组失败')

  const failureResult = await postEnvelope<AccountTestResult>(
    appBaseUrl,
    `/__aisys__/api/accounts/${failingOwnerAccount.id}/test?systemAccountId=${encodeURIComponent(grantee.id)}`,
    sessionCookie(admin.id),
    { model: 'gpt-5.5' }
  )
  assert.equal(failureResult.success, false, '授权账户测试收到上游失败时测试结果不应成功')
  assert.equal(failureResult.statusCode, 400, '授权账户测试应保留上游失败状态码用于诊断')
  assert.equal(failureResult.accountStatusChanged, true, '授权账户测试失败应返回本地状态已变更')
  assert.equal(failureResult.accountStatus, 'temporary_unavailable', '授权账户测试失败应写入被授权人本地临时不可调用')

  const failedGranteeView = repositories.findAccountSummary(failingOwnerAccount.id, granteeAccess) as AccountView | undefined
  assert.equal(failedGranteeView?.status, 'temporary_unavailable', '测试失败后被授权用户视角应为临时不可调用')
  assert.equal(failedGranteeView?.localStatus, 'temporary_unavailable', '测试失败应写入授权账户本地状态')
  assert(failedGranteeView?.localCooldownUntil, '测试失败应写入授权账户本地冷却时间')
  assert(failedGranteeView?.localLastErrorMessage?.includes('账户测试失败'), `测试失败应写入授权账户本地错误信息，实际 ${failedGranteeView?.localLastErrorMessage}`)
  assert.equal(repositories.findAccountSummary(failingOwnerAccount.id, ownerAccess)?.status, 'active', '测试失败不应修改所有者物理账户')

  console.log('授权账户测试本地状态恢复和失败隔离回归通过')
} finally {
  await closeServer(appServer)
  await closeServer(mockOpenAIServer)
  try {
    flushAllUsageRecordQueue()
    flushAllOperationLogQueue()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function createMockOpenAIServer(): http.Server {
  return http.createServer((req, res) => {
    if (req.method !== 'POST' || req.url?.split('?', 1)[0] !== '/v1/responses') {
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
            message: '切换key需要冷却30秒',
            type: 'invalid_request_error'
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

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: Record<string, unknown>): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
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
