import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import type { AccountSummary } from '../../domain/types.js'
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
  { flushAllUsageRecordQueueAsync },
  { flushAllOperationLogQueue },
  databaseModule,
  repositories,
  usageRecordWriterPool
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-record-writer-pool.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/accounts', requireAdmin, accountsRouter)

interface AccountTestResult {
  success: boolean
  statusCode?: number
  errorCode?: string
  accountFailureEligible?: boolean
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
    accountName: '手动测试模型不匹配不恢复',
    apiKey: 'sk-manual-restore-model-mismatch',
    testModel: 'gpt-5.4',
    makeUnavailable: makeActiveAccountUnschedulable
  })

  await assertManualTestRestoresAccount({
    appBaseUrl,
    mockBaseUrl,
    groupId: group.id,
    accountName: '手动测试请求形态不匹配不恢复',
    apiKey: 'sk-manual-restore-mode-mismatch',
    testEndpointMode: 'responses_json',
    makeUnavailable: makeActiveAccountUnschedulable
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
    accountName: '手动测试异常恢复',
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

  await assertInvalidProtocolEvidenceDoesNotActivatePendingAccount({
    appBaseUrl,
    mockBaseUrl,
    groupId: group.id
  })

  await assertInvalidProtocolEvidenceDegradesActiveAccount({
    appBaseUrl,
    mockBaseUrl,
    groupId: group.id
  })

  console.log('手动账号测试状态隔离回归通过：成功仅作为诊断证据，不改变账户运行状态')
} finally {
  await closeServer(appServer)
  await closeServer(mockOpenAIServer)
  try {
    await flushAllUsageRecordQueueAsync()
    await usageRecordWriterPool.closeUsageRecordWriterPool()
    flushAllOperationLogQueue()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  restoreWorkerParentIpc()
  await removeTempRoot()
}

process.exit(0)

async function assertManualTestRestoresAccount(input: {
  appBaseUrl: string
  mockBaseUrl: string
  groupId: string
  accountName: string
  apiKey: string
  testModel?: string
  testEndpointMode?: 'responses_sse' | 'responses_json'
  makeUnavailable: (accountId: string) => void
}): Promise<void> {
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: input.accountName,
    type: 'api_key',
    credentials: { api_key: input.apiKey, base_url: input.mockBaseUrl },
    supportedModels: ['gpt-5.5', 'gpt-5.4'],
    healthCheckModel: 'gpt-5.5',
    healthCheckEndpointMode: 'responses_sse',
    status: 'active',
    schedulable: true,
    groupId: input.groupId
  }, adminAccess)
  assert.equal(account.boundGroupId, input.groupId, `${input.accountName} 应绑定分组`)
  assert.equal(repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), true, `${input.accountName} 应先由后台健康检查激活`)

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
  const unavailableState = accountAvailabilityState(unavailable)

  const result = await submitAccountTestAndWait<AccountTestResult>({
    baseUrl: input.appBaseUrl,
    path: `/__aisys__/api/accounts/${account.id}/test`,
    cookie: sessionCookie(),
    body: { model: input.testModel ?? 'gpt-5.5', testEndpointMode: input.testEndpointMode ?? 'responses_sse' }
  })
  assert.equal(result.success, true, `${input.accountName} 手动测试应通过：${result.message}`)
  assert.equal(result.statusCode, 200, `${input.accountName} 应返回上游 200`)
  assert(result.traceId, `${input.accountName} 测试结果应返回本地 traceId`)
  assert.notEqual(result.accountStatusChanged, true, `${input.accountName} 手动测试成功不得标记账户状态变化`)

  const after = repositories.findAccountSummary(account.id, adminAccess)
  assert.deepEqual(
    accountAvailabilityState(after),
    unavailableState,
    `${input.accountName} 手动测试成功不得改写账户运行状态`
  )
  if (result.accountStatus !== undefined) {
    assert.equal(result.accountStatus, unavailable.status, `${input.accountName} 测试结果账户状态应保持原值`)
  }
}

async function assertInvalidProtocolEvidenceDoesNotActivatePendingAccount(input: {
  appBaseUrl: string
  mockBaseUrl: string
  groupId: string
}): Promise<void> {
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'HTTP 200 无协议完成证据',
    type: 'api_key',
    credentials: { api_key: 'sk-manual-invalid-protocol-evidence', base_url: input.mockBaseUrl },
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    healthCheckEndpointMode: 'responses_sse',
    status: 'pending_test',
    schedulable: false,
    groupId: input.groupId
  }, adminAccess)
  const before = repositories.findAccountSummary(account.id, adminAccess)
  assert.equal(before?.status, 'pending_test', '无效协议证据账户测试前应处于待检查')
  assert.equal(before?.schedulable, false, '无效协议证据账户测试前不应可调度')

  const result = await submitAccountTestAndWait<AccountTestResult>({
    baseUrl: input.appBaseUrl,
    path: `/__aisys__/api/accounts/${account.id}/test`,
    cookie: sessionCookie(),
    body: { model: 'gpt-5.5', testEndpointMode: 'responses_sse' }
  })
  assert.equal(result.success, false, '只有 [DONE] 的 HTTP 200 流响应不得判定为检查成功')
  assert.equal(result.errorCode, 'invalid_protocol_success_response', '应返回缺少协议完成证据错误码')
  assert.equal(result.accountFailureEligible, true, '上游无效协议响应应计入账户健康失败')

  const after = repositories.findAccountSummary(account.id, adminAccess)
  assert.equal(after?.status, 'pending_test', '无协议完成证据不得激活待检查账户')
  assert.equal(after?.schedulable, false, '无协议完成证据不得让待检查账户进入号池')
}

async function assertInvalidProtocolEvidenceDegradesActiveAccount(input: {
  appBaseUrl: string
  mockBaseUrl: string
  groupId: string
}): Promise<void> {
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'HTTP 200 无协议证据的正常账户',
    type: 'api_key',
    credentials: { api_key: 'sk-manual-invalid-protocol-evidence', base_url: input.mockBaseUrl },
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    healthCheckEndpointMode: 'responses_sse',
    status: 'active',
    schedulable: true,
    groupId: input.groupId
  }, adminAccess)
  assert.equal(repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 2,
    statusCode: 200
  }), true, '正常账户应先记录健康成功基线')

  let thresholdFailure: ReturnType<typeof repositories.recordAccountHealthCheckFailure> | undefined
  for (let attempt = 1; attempt <= 2; attempt += 1) {
    const observedAt = new Date().toISOString()
    const result = await submitAccountTestAndWait<AccountTestResult>({
      baseUrl: input.appBaseUrl,
      path: `/__aisys__/api/accounts/${account.id}/test`,
      cookie: sessionCookie(),
      body: { model: 'gpt-5.5', testEndpointMode: 'responses_sse' }
    })
    assert.equal(result.success, false, `正常账户第 ${attempt} 次无效协议响应应失败`)
    assert.equal(result.accountFailureEligible, true, `正常账户第 ${attempt} 次无效协议响应应计入失败阈值`)
    thresholdFailure = repositories.recordAccountHealthCheckFailure(account.id, {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 2,
      errorCode: result.errorCode,
      errorMessage: result.message,
      countTowardsThreshold: result.accountFailureEligible,
      observedAt
    })
    assert.equal(thresholdFailure.failureCount, attempt, `正常账户第 ${attempt} 次协议失败应正确累计`)
  }
  assert.equal(thresholdFailure?.reachedThreshold, true, '无效协议响应累计后应达到健康失败阈值')

  const active = repositories.findAccountSummary(account.id, adminAccess)
  assert(active?.configRevision, '达到阈值的正常账户应包含配置版本')
  const degraded = repositories.markAccountTestTemporaryUnavailable(
    active,
    '无效协议响应达到健康失败阈值',
    adminAccess,
    {
      configRevision: active.configRevision,
      checkedAt: thresholdFailure!.checkedAt,
      failureCount: thresholdFailure!.failureCount,
      observedAt: thresholdFailure!.checkedAt
    }
  )
  assert.equal(degraded?.status, 'temporary_unavailable', 'active 账户无效协议响应达到阈值后应临时停调')
}

function accountAvailabilityState(account: AccountSummary | undefined) {
  return {
    status: account?.status,
    schedulable: account?.schedulable,
    cooldownUntil: account?.cooldownUntil,
    lastErrorCode: account?.lastErrorCode,
    lastErrorMessage: account?.lastErrorMessage,
    lastErrorTraceId: account?.lastErrorTraceId,
    cooldownRetestFailureCount: account?.cooldownRetestFailureCount,
    cooldownRetestObservationStartedAt: account?.cooldownRetestObservationStartedAt,
    cooldownRetestGeneration: account?.cooldownRetestGeneration,
    cooldownRetestDispatchRevision: account?.cooldownRetestDispatchRevision,
    cooldownRetestSourceConfigRevision: account?.cooldownRetestSourceConfigRevision,
    cooldownRetestLastAt: account?.cooldownRetestLastAt,
    cooldownRetestLastStatusCode: account?.cooldownRetestLastStatusCode,
    temporaryUnavailableContinuousProbeEnabled: account?.temporaryUnavailableContinuousProbeEnabled,
    lastHealthCheckAt: account?.lastHealthCheckAt,
    nextHealthCheckAt: account?.nextHealthCheckAt,
    lastHealthSuccessAt: account?.lastHealthSuccessAt,
    healthCheckFailureCount: account?.healthCheckFailureCount,
    healthCheckFailureStartedAt: account?.healthCheckFailureStartedAt,
    lastHealthCheckStatusCode: account?.lastHealthCheckStatusCode,
    lastHealthCheckErrorCode: account?.lastHealthCheckErrorCode,
    lastHealthCheckErrorMessage: account?.lastHealthCheckErrorMessage,
    lastHealthCheckTraceId: account?.lastHealthCheckTraceId,
    streamFailureCount: account?.streamFailureCount,
    streamFailureWindowStartedAt: account?.streamFailureWindowStartedAt
  }
}

function makeActiveAccountUnschedulable(accountId: string): void {
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
}

function createMockOpenAIServer(): http.Server {
  return http.createServer((req, res) => {
    const requestPath = req.url?.split('?', 1)[0]
    if (req.method === 'GET' && requestPath === '/v1/models') {
      res.writeHead(200, { 'content-type': 'application/json' })
      res.end(JSON.stringify({
        object: 'list',
        data: [{ id: 'gpt-5.5', object: 'model' }, { id: 'gpt-5.4', object: 'model' }]
      }))
      return
    }
    if (req.method !== 'POST' || (requestPath !== '/v1/responses' && requestPath !== '/responses')) {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'not found' } }))
      return
    }
    const requestChunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => {
      requestChunks.push(chunk)
    })
    req.on('end', () => {
      if (req.headers.authorization?.includes('sk-manual-invalid-protocol-evidence')) {
        res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
        res.end('data: [DONE]\n\n')
        return
      }
      const requestBody = JSON.parse(Buffer.concat(requestChunks).toString('utf8')) as { stream?: boolean }
      const completedEvent = {
        type: 'response.completed',
        response: {
          id: 'resp_manual_test_restore',
          object: 'response',
          status: 'completed',
          output: [
            {
              type: 'message',
              content: [{ type: 'output_text', text: expectedProbeOutput(JSON.stringify(requestBody)) }]
            }
          ],
          usage: {
            input_tokens: 1,
            output_tokens: 1,
            total_tokens: 2
          }
        }
      }
      if (requestBody.stream === false) {
        res.writeHead(200, { 'content-type': 'application/json' })
        res.end(JSON.stringify(completedEvent.response))
        return
      }
      res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
      res.end(`event: response.completed\ndata: ${JSON.stringify(completedEvent)}\n\n`)
    })
    req.resume()
  })
}

function expectedProbeOutput(requestText: string): string {
  const match = /juhe\d{3}/.exec(requestText)
  return match?.[0] ?? 'juhe000'
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

async function removeTempRoot(): Promise<void> {
  for (let attempt = 0; attempt < 8; attempt += 1) {
    try {
      rmSync(tempRoot, { recursive: true, force: true })
      return
    } catch (error) {
      if (!(error instanceof Error) || !/EBUSY|EPERM/.test(error.message)) {
        throw error
      }
      if (attempt === 7) return
      await new Promise((resolvePromise) => setTimeout(resolvePromise, 100 + attempt * 100))
    }
  }
}
