import { mkdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'
import http from 'node:http'

import cors from 'cors'
import express, { type NextFunction, type Request, type Response as ExpressResponse } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { ok } from '../../shared/http.js'
import { logger } from '../../shared/logger.js'
import { submitAccountTestAndWait } from '../shared/account-test-task-client.js'
import { installWorkerParentIpcHarness } from '../shared/worker-parent-ipc-harness.js'
import { DEFAULT_OPENAI_SUPPORTED_MODELS } from '../../storage/schema-defaults.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-proxy-negative-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'proxy-negative.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'proxy-negative-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const restoreWorkerParentIpc = installWorkerParentIpcHarness()

const [
  { accountsRouter },
  { apiKeysRouter },
  { authRouter },
  { captchaAnswerForTest },
  { groupsRouter },
  { openAIGatewayRouter },
  { proxiesRouter },
  { routeStrategiesRouter },
  { usageRecordsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  gatewayCache,
  usageRecordQueue,
  usageRecordWriterPool
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/api-keys/api-keys.routes.js'),
  import('../../modules/auth/auth.routes.js'),
  import('../../modules/auth/captcha.service.js'),
  import('../../modules/groups/groups.routes.js'),
  import('../../modules/gateway/routes.js'),
  import('../../modules/proxies/proxies.routes.js'),
  import('../../modules/route-strategies/route-strategies.routes.js'),
  import('../../modules/usage-records/usage-records.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/usage-record-writer-pool.js')
])

const gatewayRawBodyLimit = '8mb'
const app = express()
app.use(requestContextMiddleware)
app.use(cors({ credentials: true, origin: true }))
app.use('/v1', express.raw({ type: () => true, limit: gatewayRawBodyLimit }), captureGatewayRawBody, openAIGatewayRouter)
app.use(express.json({ limit: '2mb' }))
app.use('/__aisys__/api/auth', authRouter)
app.use('/__aisys__/api/settings/public', (_req, res) => {
  res.json(ok(repositories.listPublicGlobalSettings()))
})
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/__aisys__/api/my-groups', forceSelfAccessScope, groupsRouter)
app.use('/__aisys__/api/my-api-keys', forceSelfAccessScope, apiKeysRouter)
app.use('/__aisys__/api/accounts', requireAdmin, accountsRouter)
app.use('/__aisys__/api/groups', requireAdmin, groupsRouter)
app.use('/__aisys__/api/api-keys', requireAdmin, apiKeysRouter)
app.use('/__aisys__/api/route-strategies', requireAdmin, routeStrategiesRouter)
app.use('/__aisys__/api/proxies', proxiesRouter)
app.use('/__aisys__/api/usage-records', requireAdmin, usageRecordsRouter)

type RawBodyRequest = Request & { rawBody?: Buffer }

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface ProxyProfileSummary {
  id: string
  name: string
  enabled: boolean
  updatedAt: string
}

interface ProxyProfileMutationResult {
  id: string
  updatedAt: string
  changed: boolean
  values: { enabled?: boolean }
}

interface AccountSummary {
  id: string
  name: string
  configRevision?: number
  changedFields?: string[]
  status?: string
  schedulable?: boolean
  cooldownUntil?: string
  lastErrorMessage?: string
  ownerSystemAccountId?: string
  proxyProfileId?: string
}

interface GroupSummary {
  id: string
  name: string
}

interface ApiKeySummary {
  id: string
  name: string
  key?: string
  routeStrategyId?: string
}

interface RouteStrategySummary {
  id: string
  name: string
  groupBindings?: Array<{ groupId: string }>
}

interface AccountTestResult {
  success: boolean
  message: string
  model?: string
  proxyUrl?: string
  accountStatusChanged?: boolean
  accountStatus?: string
}

interface AccountTestTask<T = AccountTestResult> {
  id: string
  status: 'queued' | 'running' | 'success' | 'failed' | 'canceled'
  message?: string
  result?: T
}

const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
const proxyRegressionModel = DEFAULT_OPENAI_SUPPORTED_MODELS[0]

async function main(): Promise<void> {
  let appServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  try {
    upstreamServer = createDirectUpstreamServer()
    await listen(upstreamServer)
    const upstreamAddress = serverAddress(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${upstreamAddress.port}/v1`

    appServer = app.listen(0, '127.0.0.1')
    await listen(appServer)
    const appAddress = serverAddress(appServer)
    const baseUrl = `http://127.0.0.1:${appAddress.port}`

    const adminCookie = await login(baseUrl)
    const proxy = await postEnvelope<ProxyProfileSummary>(baseUrl, '/__aisys__/api/proxies', adminCookie, {
      name: '回归停用代理',
      type: 'http',
      host: '127.0.0.1',
      port: 9,
      enabled: true
    })
    const group = await postEnvelope<GroupSummary>(baseUrl, '/__aisys__/api/groups', adminCookie, {
      name: '代理负向回归分组',
      providerCode: 'gpt',
      enabled: true
    })
    const accountPayload = {
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '代理负向回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-proxy-negative',
        base_url: upstreamBaseUrl
      },
      supportedModels: [proxyRegressionModel],
      healthCheckModel: proxyRegressionModel,
      healthCheckEndpointMode: 'responses_json',
      groupId: group.id
    }
    const manualDraftTask = await submitDraftAccountTestAndWait(baseUrl, adminCookie, accountPayload)
    assert(manualDraftTask.result?.success === true, `代理负向账户草稿人工测试应通过：${manualDraftTask.result?.message ?? manualDraftTask.message ?? ''}`)
    const createdAccount = await postEnvelope<AccountSummary>(baseUrl, '/__aisys__/api/accounts', adminCookie, accountPayload)
    assert(createdAccount.status === 'pending_test', '草稿人工测试成功不能激活新账户')
    const createdAccountSnapshot = repositories.findAccountSummary(createdAccount.id, adminAccess)
    assert(createdAccountSnapshot?.status === 'pending_test' && createdAccountSnapshot.schedulable === false, '草稿人工测试成功后新账户必须保持待检查且不可调度')
    assert(repositories.projectAccountHealthFixtureSuccess(createdAccount.id, {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3,
      statusCode: 200
    }), '后台检查成功应激活代理负向账户')
    const account = repositories.findAccountSummary(createdAccount.id, adminAccess)
    assert(account?.status === 'active' && account.schedulable === true, '后台检查成功后代理负向账户应正常可调度')
    assert(typeof account.configRevision === 'number' && Number.isSafeInteger(account.configRevision) && account.configRevision >= 1, '代理负向账户必须返回有效配置版本')
    const expectedConfigRevision = account.configRevision
    const proxiedAccount = await patchEnvelope<AccountSummary>(baseUrl, `/__aisys__/api/accounts/${account.id}`, adminCookie, {
      expectedConfigRevision,
      proxyProfileId: proxy.id
    })
    assert(proxiedAccount.changedFields?.includes('proxyProfileId'), '代理负向账户应成功绑定代理')
    assert(proxiedAccount.configRevision === expectedConfigRevision + 1, '代理变更后配置版本必须严格递增')
    const proxiedAccountSnapshot = repositories.findAccountSummary(account.id, adminAccess)
    assert(proxiedAccountSnapshot?.proxyProfileId === proxy.id, '代理负向账户应成功绑定代理')
    assert(proxiedAccountSnapshot.status === 'pending_test', '代理变更后账户应重新进入待检查')
    assert(proxiedAccountSnapshot.configRevision === proxiedAccount.configRevision, '代理变更后的持久态配置版本必须与响应一致')
    assert(repositories.projectAccountHealthFixtureSuccess(account.id, {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3,
      statusCode: 200
    }), '代理变更后的后台检查成功应恢复账户')
    directUpstreamHitCount = 0
    const routeStrategy = await postEnvelope<RouteStrategySummary>(baseUrl, '/__aisys__/api/route-strategies', adminCookie, {
      name: '代理负向回归普通路由',
      mode: 'normal',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    })
    const routeStrategyDetail = repositories.findRouteStrategySummary(routeStrategy.id, adminAccess)
    assert(routeStrategyDetail?.groupBindings.some((binding) => binding.groupId === group.id), '代理负向策略路由应绑定当前分组')
    const apiKey = await postEnvelope<ApiKeySummary>(baseUrl, '/__aisys__/api/api-keys', adminCookie, {
      name: '代理负向回归 Key',
      routeStrategyId: routeStrategy.id,
      status: 'active'
    })
    assert(repositories.findApiKeySummary(apiKey.id, adminAccess)?.routeStrategyId === routeStrategy.id, '代理负向 API Key 应绑定当前策略路由')
    assert(apiKey.key, '临时 API Key 未返回明文密钥')

    const disabledProxy = await patchEnvelope<ProxyProfileMutationResult>(baseUrl, `/__aisys__/api/proxies/${proxy.id}`, adminCookie, {
      enabled: false,
      expectedUpdatedAt: proxy.updatedAt
    })
    assert(disabledProxy.changed && disabledProxy.values.enabled === false, '代理停用 PATCH 应返回最小 mutation 和新 CAS 版本')
    gatewayCache.clearGatewayRuntimeCache()

    const testResult = await withWorkerRole(() => submitAccountTestAndWait<AccountTestResult>({
      baseUrl,
      path: `/__aisys__/api/accounts/${account.id}/test`,
      cookie: adminCookie,
      body: {
        model: proxyRegressionModel,
        prompt: 'hi'
      }
    }))
    assert(testResult.success === false, '账户测试在代理停用后不应成功')
    assert(testResult.proxyUrl === '[configured]', '账户测试失败结果应保留代理已配置标记')
    assert(testResult.message.includes('代理不存在或已停用'), `账户测试错误信息异常：${testResult.message}`)
    assert(testResult.accountStatusChanged === false, '人工账户测试失败不能标记账户状态变化')
    const preservedAccount = repositories.findAccountSummary(account.id, adminAccess)
    assert(preservedAccount, '代理失败后账户应仍存在')
    assert(preservedAccount.status === 'active' && preservedAccount.schedulable === true, '人工账户测试失败不能改变账户可用状态')
    assert(!preservedAccount.cooldownUntil, '人工账户测试失败不能写入冷却结束时间')
    assert(!preservedAccount.lastErrorMessage, '人工账户测试失败不能写入最近错误')
    gatewayCache.clearGatewayRuntimeCache()

    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    let gatewayResponse: Record<string, unknown> & { status: number }
    try {
      gatewayResponse = await withProcessRole('db-service', async () => {
        const response = await requestJson<Record<string, unknown>>(baseUrl, '/v1/responses', {
          method: 'POST',
          headers: {
            authorization: `Bearer ${apiKey.key}`,
            'content-type': 'application/json'
          },
          body: JSON.stringify({
            model: proxyRegressionModel,
            input: 'hi',
            stream: false
          })
        })
        await usageRecordQueue.flushAllUsageRecordQueueAsync()
        return response
      })
    } finally {
      usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
    }
    assert(gatewayResponse.status === 503, `停用代理网关请求应失败为 503，实际 ${gatewayResponse.status}`)
    const gatewayResponseText = JSON.stringify(gatewayResponse)
    assert(gatewayResponseText.includes('service_unavailable'), `停用代理网关请求应返回统一服务不可用错误，实际 ${gatewayResponseText}`)
    assert(directUpstreamHitCount === 0, `停用代理后发生了直连上游请求 ${directUpstreamHitCount} 次`)

    const enabledProxy = await patchEnvelope<ProxyProfileMutationResult>(baseUrl, `/__aisys__/api/proxies/${proxy.id}`, adminCookie, {
      enabled: true,
      expectedUpdatedAt: disabledProxy.updatedAt
    })
    assert(enabledProxy.changed && enabledProxy.values.enabled === true, '代理恢复 PATCH 应串接停用 mutation 的 CAS 版本')
    await deleteNoContent(baseUrl, `/__aisys__/api/proxies/${proxy.id}`, adminCookie, 409)

    console.log('代理负向回归通过：停用代理阻止账户测试，网关请求失败，没有直连上游，使用中的代理不能删除')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    await usageRecordWriterPool.closeUsageRecordWriterPool()
    await closeServer(appServer)
    await closeServer(upstreamServer)
    try {
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    restoreWorkerParentIpc()
    await removeTempRoot()
  }
}

let directUpstreamHitCount = 0

function createDirectUpstreamServer(): http.Server {
  return http.createServer((req, res) => {
    directUpstreamHitCount += 1
    if (req.url === '/v1/responses') {
      const chunks: Buffer[] = []
      req.on('data', (chunk: Buffer | string) => chunks.push(Buffer.from(chunk)))
      req.on('end', () => {
        if (req.method !== 'POST') {
          res.writeHead(405, { 'content-type': 'application/json' })
          res.end(JSON.stringify({ error: 'responses probe must use POST' }))
          return
        }
        if (req.headers.authorization !== 'Bearer sk-proxy-negative') {
          res.writeHead(401, { 'content-type': 'application/json' })
          res.end(JSON.stringify({ error: 'responses probe authorization mismatch' }))
          return
        }
        let body: Record<string, unknown>
        try {
          const parsed: unknown = JSON.parse(Buffer.concat(chunks).toString('utf8'))
          if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) throw new Error('body must be an object')
          body = parsed as Record<string, unknown>
        } catch {
          res.writeHead(400, { 'content-type': 'application/json' })
          res.end(JSON.stringify({ error: 'responses probe body must be valid JSON' }))
          return
        }
        const firstInput = Array.isArray(body.input) && body.input.length > 0 && typeof body.input[0] === 'object' && body.input[0] !== null
          ? body.input[0] as Record<string, unknown>
          : undefined
        const content = Array.isArray(firstInput?.content) && firstInput.content.length > 0 && typeof firstInput.content[0] === 'object' && firstInput.content[0] !== null
          ? firstInput.content[0] as Record<string, unknown>
          : undefined
        if (body.model !== proxyRegressionModel || content?.text !== '只能回复：juhe') {
          res.writeHead(400, { 'content-type': 'application/json' })
          res.end(JSON.stringify({ error: 'responses probe request contract mismatch' }))
          return
        }
        res.writeHead(200, { 'content-type': 'application/json' })
        res.end(JSON.stringify({
          id: 'resp_proxy_negative',
          object: 'response',
          status: 'completed',
          model: proxyRegressionModel,
          output: [{
            type: 'message',
            content: [{ type: 'output_text', text: 'juhe' }]
          }],
          usage: {
            input_tokens: 1,
            output_tokens: 1
          }
        }))
      })
      return
    }
    res.writeHead(200, { 'content-type': 'application/json' })
    res.end(JSON.stringify({ object: 'list', data: [] }))
  })
}

function captureGatewayRawBody(req: RawBodyRequest, _res: ExpressResponse, next: NextFunction): void {
  const rawBody = Buffer.isBuffer(req.body) ? Buffer.from(req.body) : Buffer.alloc(0)
  req.rawBody = rawBody
  const contentType = req.headers['content-type'] ?? ''
  if (rawBody.length > 0 && String(contentType).toLowerCase().includes('json')) {
    try {
      req.body = JSON.parse(rawBody.toString('utf8')) as unknown
    } catch {
      req.body = undefined
    }
  } else {
    req.body = undefined
  }
  next()
}

async function login(baseUrl: string): Promise<string> {
  const captcha = await getEnvelope<{ captchaId: string; image: string }>(baseUrl, '/__aisys__/api/auth/captcha')
  const captchaCode = captchaAnswerForTest(captcha.captchaId)
  assert(captchaCode, '测试夹具无法读取登录验证码')
  const response = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      username: 'admin',
      password: 'admin',
      captchaId: captcha.captchaId,
      captchaCode
    })
  })
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert(response.ok, `登录失败：HTTP ${response.status} ${await response.text()}`)
  assert(cookie, '登录未返回会话 Cookie')
  const passwordResponse = await fetch(`${baseUrl}/__aisys__/api/auth/change-password`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify({ oldPassword: 'admin', newPassword: 'admin-regression-password' })
  })
  assert(passwordResponse.ok, `回归夹具修改初始密码失败：HTTP ${passwordResponse.status} ${await passwordResponse.text()}`)
  return cookie
}

async function submitDraftAccountTestAndWait(baseUrl: string, cookie: string, account: Record<string, unknown>): Promise<AccountTestTask<AccountTestResult>> {
  const task = await postEnvelope<AccountTestTask<AccountTestResult>>(baseUrl, '/__aisys__/api/accounts/test-draft', cookie, {
    account
  })
  return await waitForAccountTestTask(baseUrl, cookie, task.id)
}

async function waitForAccountTestTask(baseUrl: string, cookie: string, taskId: string): Promise<AccountTestTask<AccountTestResult>> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 30_000) {
    const tasks = await getEnvelope<Array<AccountTestTask<AccountTestResult>>>(
      baseUrl,
      `/__aisys__/api/accounts/test-tasks?ids=${encodeURIComponent(taskId)}`,
      cookie
    )
    const task = tasks.find((item) => item.id === taskId)
    assert(task, `账号测试任务 ${taskId} 应可查询`)
    if (task.status === 'success' || task.status === 'failed') {
      assert(task.result, `账号测试任务 ${taskId} 已结束但没有结果`)
      return task
    }
    if (task.status === 'canceled') {
      throw new Error(`账号测试任务 ${taskId} 已取消：${task.message ?? ''}`)
    }
    await sleep(100)
  }
  throw new Error(`账号测试任务 ${taskId} 等待超时`)
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie?: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, cookie ? { headers: { cookie } } : undefined)
  return unwrapEnvelope<T>(response, path)
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  return unwrapEnvelope<T>(response, path)
}

async function withWorkerRole<T>(action: () => Promise<T>): Promise<T> {
  return await withProcessRole('worker', action)
}

async function withProcessRole<T>(role: typeof runtimeConfig.processRole, action: () => Promise<T>): Promise<T> {
  const previousProcessRole = runtimeConfig.processRole
  try {
    runtimeConfig.processRole = role
    return await action()
  } finally {
    runtimeConfig.processRole = previousProcessRole
  }
}

async function patchEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  return unwrapEnvelope<T>(response, path)
}

async function deleteNoContent(baseUrl: string, path: string, cookie: string, expectedStatus = 204): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, { method: 'DELETE', headers: { cookie } })
  const text = await response.text()
  assert(response.status === expectedStatus, `${path} HTTP ${response.status}: ${text}`)
}

async function requestJson<T>(baseUrl: string, path: string, init?: RequestInit): Promise<T & { status: number }> {
  const response = await fetch(`${baseUrl}${path}`, init)
  const text = await response.text()
  let body: T
  try {
    body = JSON.parse(text) as T
  } catch {
    body = {} as T
  }
  return { ...body, status: response.status }
}

async function unwrapEnvelope<T>(response: Response, path: string): Promise<T> {
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

function listen(server: http.Server): Promise<void> {
  server.listen(0, '127.0.0.1')
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('服务地址不可用')
  }
  return { port: address.port }
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
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
      await sleep(100 + attempt * 100)
    }
  }
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

main().then(() => {
  process.exit(0)
}).catch((error) => {
  console.error('\n代理负向回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exit(1)
})
