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

const tempRoot = resolve(tmpdir(), `juhe-ai-account-test-local-restore-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-test-local-restore.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-test-local-restore-secret'
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
  { requireAdmin, requireAuth },
  { requestContextMiddleware },
  { flushAllUsageRecordQueue },
  { flushAllOperationLogQueue },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/accounts', requireAdmin, accountsRouter)

interface AccountTestResult {
  success: boolean
  statusCode?: number
  accountStatusChanged?: boolean
  accountStatus?: string
  traceId?: string
  message: string
}

const admin = repositories.createSystemAccount({
  username: 'account_test_local_restore_admin',
  displayName: '手动测试恢复管理员',
  password: 'password',
  role: 'admin',
  status: 'active',
  mustChangePassword: false
})
const adminAccess = { systemAccountId: admin.id, role: 'admin' as const }
let appServer: ReturnType<typeof app.listen> | undefined
let mockOpenAIServer: http.Server | undefined

try {
  mockOpenAIServer = createMockOpenAIServer()
  mockOpenAIServer.listen(0, '127.0.0.1')
  await onceListening(mockOpenAIServer)
  const mockAddress = mockOpenAIServer.address()
  if (!mockAddress || typeof mockAddress === 'string') {
    throw new Error('手动测试恢复 mock 上游地址不可用')
  }
  const mockBaseUrl = `http://127.0.0.1:${mockAddress.port}`

  appServer = app.listen(0, '127.0.0.1')
  await onceListening(appServer)
  const appAddress = appServer.address()
  if (!appAddress || typeof appAddress === 'string') {
    throw new Error('手动测试恢复服务地址不可用')
  }
  const appBaseUrl = `http://127.0.0.1:${appAddress.port}`

  const group = repositories.createGroup({
    name: '手动测试恢复分组',
    providerCode: 'gpt'
  }, adminAccess)

  await assertManualTestRestoresAccount({
    appBaseUrl,
    mockBaseUrl,
    groupId: group.id,
    accountName: '手动测试恢复临时不可调用',
    apiKey: 'sk-manual-restore-temporary',
    makeUnavailable: (accountId) => {
      const updated = repositories.markAccountTemporaryUnavailable(accountId, '模拟临时不可调用')
      assert.equal(updated?.status, 'temporary_unavailable', '应能把账户置为临时不可调用')
    }
  })

  await assertManualTestRestoresAccount({
    appBaseUrl,
    mockBaseUrl,
    groupId: group.id,
    accountName: '手动测试恢复限流',
    apiKey: 'sk-manual-restore-rate-limited',
    makeUnavailable: (accountId) => {
      const updated = repositories.markAccountCooldown(
        accountId,
        new Date(Date.now() + 60_000).toISOString(),
        '模拟限流',
        'rate_limited'
      )
      assert.equal(updated?.status, 'rate_limited', '应能把账户置为限流')
    }
  })

  await assertManualTestRestoresAccount({
    appBaseUrl,
    mockBaseUrl,
    groupId: group.id,
    accountName: '手动测试恢复异常',
    apiKey: 'sk-manual-restore-error',
    makeUnavailable: (accountId) => {
      const updated = repositories.markAccountException(accountId, 'upstream_failure', '模拟异常')
      assert.equal(updated?.status, 'error', '应能把账户置为异常')
    }
  })

  await assertManualTestRestoresAccount({
    appBaseUrl,
    mockBaseUrl,
    groupId: group.id,
    accountName: '手动测试恢复不可调度',
    apiKey: 'sk-manual-restore-unschedulable',
    makeUnavailable: (accountId) => {
      databaseModule.getBusinessDatabase()
        .prepare(`
          UPDATE accounts
          SET status = 'active',
              schedulable = 0,
              updated_at = ?
          WHERE id = ?
            AND deleted_at IS NULL
        `)
        .run(new Date().toISOString(), accountId)
      const updated = repositories.findAccountSummary(accountId, adminAccess)
      assert.equal(updated?.status, 'active', '应保持账户状态为正常')
      assert.equal(updated?.schedulable, false, '应能把账户置为不可调度')
    }
  })

  console.log('手动账号测试恢复回归通过：自有账户测试成功会恢复临时不可调用、限流、异常和不可调度状态')
} finally {
  await closeServer(appServer)
  await closeServer(mockOpenAIServer)
  try {
    flushAllUsageRecordQueue()
    flushAllOperationLogQueue()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  restoreWorkerParentIpc()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertManualTestRestoresAccount(input: {
  appBaseUrl: string
  mockBaseUrl: string
  groupId: string
  accountName: string
  apiKey: string
  makeUnavailable: (accountId: string) => void
}): Promise<void> {
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: input.accountName,
    type: 'api_key',
    credentials: { api_key: input.apiKey, base_url: input.mockBaseUrl },
    status: 'active',
    schedulable: true,
    groupId: input.groupId
  }, adminAccess)
  assert.equal(account.boundGroupId, input.groupId, `${input.accountName} 应绑定分组`)

  input.makeUnavailable(account.id)
  const unavailable = repositories.findAccountSummary(account.id, adminAccess)
  assert(unavailable, `${input.accountName} 不存在`)
  assert(
    unavailable.status !== 'active'
      || unavailable.schedulable === false
      || Boolean(unavailable.cooldownUntil)
      || Boolean(unavailable.lastErrorCode)
      || Boolean(unavailable.lastErrorMessage),
    `${input.accountName} 应先处于不可调用状态`
  )

  const result = await submitAccountTestAndWait<AccountTestResult>({
    baseUrl: input.appBaseUrl,
    path: `/__aisys__/api/accounts/${account.id}/test`,
    cookie: sessionCookie(),
    body: { model: 'gpt-5.5' }
  })
  assert.equal(result.success, true, `${input.accountName} 手动测试应通过：${result.message}`)
  assert.equal(result.statusCode, 200, `${input.accountName} 应返回上游 200`)
  assert.equal(result.accountStatusChanged, true, `${input.accountName} 测试结果应标记状态已恢复`)
  assert.equal(result.accountStatus, 'active', `${input.accountName} 测试结果状态应为正常`)
  assert(result.traceId, `${input.accountName} 测试结果应返回本地 traceId`)
  flushAllUsageRecordQueue()
  const usageRecordsByTrace = repositories.listUsageRecords(adminAccess, {
    traceId: result.traceId,
    trafficSource: 'manual_account_test',
    page: 1,
    pageSize: 10
  })
  assert(
    usageRecordsByTrace.items.some((item) => item.traceId === result.traceId && item.accountId === account.id && item.trafficSource === 'manual_account_test'),
    `${input.accountName} 应能通过测试返回的 traceId 查到手动测试使用记录`
  )

  const restored = repositories.findAccountSummary(account.id, adminAccess)
  assert.equal(restored?.status, 'active', `${input.accountName} 测试成功后应恢复正常`)
  assert.equal(restored?.schedulable, true, `${input.accountName} 测试成功后应恢复调度`)
  assert.equal(restored?.cooldownUntil, undefined, `${input.accountName} 测试成功后应清理冷却时间`)
  assert.equal(restored?.lastErrorCode, undefined, `${input.accountName} 测试成功后应清理错误码`)
  assert.equal(restored?.lastErrorMessage, undefined, `${input.accountName} 测试成功后应清理错误信息`)
}

function createMockOpenAIServer(): http.Server {
  return http.createServer((req, res) => {
    if (req.method !== 'POST' || req.url?.split('?', 1)[0] !== '/v1/responses') {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'not found' } }))
      return
    }
    req.on('end', () => {
      const completedEvent = {
        type: 'response.completed',
        response: {
          id: 'resp_manual_test_restore',
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

function sessionCookie(): string {
  return `juhe_ai_session=${repositories.createSession(adminAccess.systemAccountId, 1).token}`
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
