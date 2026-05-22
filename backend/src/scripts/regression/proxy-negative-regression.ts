import { mkdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'
import http from 'node:http'

import cors from 'cors'
import express, { type NextFunction, type Request, type Response as ExpressResponse } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { ok } from '../../shared/http.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-proxy-negative-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'proxy-negative.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'proxy-negative-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { accountsRouter },
  { apiKeysRouter },
  { authRouter },
  { groupsRouter },
  { openAIGatewayRouter },
  { proxiesRouter },
  { usageRecordsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  gatewayCache,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/api-keys/api-keys.routes.js'),
  import('../../modules/auth/auth.routes.js'),
  import('../../modules/groups/groups.routes.js'),
  import('../../modules/gateway/openai-gateway.routes.js'),
  import('../../modules/proxies/proxies.routes.js'),
  import('../../modules/usage-records/usage-records.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/gateway-runtime-cache.service.js'),
  import('../../modules/gateway/usage-record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
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
}

interface AccountSummary {
  id: string
  name: string
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
}

interface AccountTestResult {
  success: boolean
  message: string
  proxyUrl?: string
  accountStatusChanged?: boolean
  accountStatus?: string
}

interface UsageRecordListResult {
  items: Array<{
    apiKeyId?: string
    accountId?: string
    endpoint?: string
    statusCode?: number
    success: boolean
    errorMessage?: string
  }>
  total: number
}

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
    const account = await postEnvelope<AccountSummary>(baseUrl, '/__aisys__/api/accounts', adminCookie, {
      providerCode: 'openai',
      name: '代理负向回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-proxy-negative',
        base_url: upstreamBaseUrl
      },
      proxyProfileId: proxy.id,
      status: 'active',
      schedulable: true
    })
    const group = await postEnvelope<GroupSummary>(baseUrl, '/__aisys__/api/groups', adminCookie, {
      name: '代理负向回归分组',
      providerCode: 'openai',
      enabled: true
    })
    await postEnvelope<AccountSummary>(baseUrl, `/__aisys__/api/accounts/${account.id}/group`, adminCookie, { groupId: group.id })
    const apiKey = await postEnvelope<ApiKeySummary>(baseUrl, '/__aisys__/api/api-keys', adminCookie, {
      name: '代理负向回归 Key',
      groupId: group.id,
      status: 'active'
    })
    assert(apiKey.key, '临时 API Key 未返回明文密钥')

    await patchEnvelope<ProxyProfileSummary>(baseUrl, `/__aisys__/api/proxies/${proxy.id}`, adminCookie, { enabled: false })
    gatewayCache.clearGatewayRuntimeCache()

    const testResult = await postEnvelope<AccountTestResult>(baseUrl, `/__aisys__/api/accounts/${account.id}/test`, adminCookie, {
      model: 'gpt-4o-mini',
      prompt: 'hi'
    })
    assert(testResult.success === false, '账户测试在代理停用后不应成功')
    assert(testResult.proxyUrl === '[configured]', '账户测试失败结果应保留代理已配置标记')
    assert(testResult.message.includes('代理不存在或已停用'), `账户测试错误信息异常：${testResult.message}`)
    assert(testResult.accountStatusChanged === true, '账户测试确认账号不可用后应返回状态已变更')
    assert(testResult.accountStatus === 'temporary_unavailable', `账户测试失败后应标记临时不可调用，实际 ${testResult.accountStatus}`)
    const cooledAccount = repositories.findAccountSummary(account.id)
    assert(cooledAccount?.status === 'temporary_unavailable', `账户测试失败后数据库状态应为临时不可调用，实际 ${cooledAccount?.status}`)
    assert(Boolean(cooledAccount?.cooldownUntil), '账户测试失败后应写入冷却结束时间')
    assert(cooledAccount?.lastErrorMessage?.includes('账户测试失败'), `账户测试失败后应写入最近错误，实际 ${cooledAccount?.lastErrorMessage}`)
    await patchEnvelope<AccountSummary>(baseUrl, `/__aisys__/api/accounts/${account.id}`, adminCookie, {
      status: 'active',
      schedulable: true,
      clearFailureState: true
    })
    gatewayCache.clearGatewayRuntimeCache()

    const gatewayResponse = await requestJson<Record<string, unknown>>(baseUrl, '/v1/responses', {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        input: 'hi',
        stream: false
      })
    })
    assert(gatewayResponse.status === 503, `停用代理网关请求应失败为 503，实际 ${gatewayResponse.status}`)
    assert(directUpstreamHitCount === 0, `停用代理后发生了直连上游请求 ${directUpstreamHitCount} 次`)

    usageRecordQueue.flushAllUsageRecordQueue()
    await waitForUsageRecord(baseUrl, adminCookie, apiKey.id, account.id)

    await patchEnvelope<ProxyProfileSummary>(baseUrl, `/__aisys__/api/proxies/${proxy.id}`, adminCookie, { enabled: true })
    await deleteNoContent(baseUrl, `/__aisys__/api/proxies/${proxy.id}`, adminCookie, 409)

    console.log('代理负向回归通过：停用代理阻止账户测试，网关请求失败，没有直连上游，使用中的代理不能删除')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(appServer)
    await closeServer(upstreamServer)
    try {
      databaseModule.getDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

let directUpstreamHitCount = 0

function createDirectUpstreamServer(): http.Server {
  return http.createServer((req, res) => {
    directUpstreamHitCount += 1
    if (req.url === '/v1/responses') {
      res.writeHead(200, { 'content-type': 'application/json' })
      res.end(JSON.stringify({
        id: 'resp_proxy_negative',
        object: 'response',
        status: 'completed',
        model: 'gpt-4o-mini',
        output: [],
        usage: {
          input_tokens: 1,
          output_tokens: 1
        }
      }))
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
  const captchaCode = parseCaptchaCode(captcha.image)
  assert(captchaCode, '无法解析登录验证码')
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
  return cookie
}

function parseCaptchaCode(image: string): string {
  const base64 = image.replace(/^data:image\/svg\+xml;base64,/, '')
  const svg = Buffer.from(base64, 'base64').toString('utf8')
  return [...svg.matchAll(/<text[^>]*>([^<]+)<\/text>/g)].map((match) => match[1]).join('')
}

async function waitForUsageRecord(baseUrl: string, cookie: string, apiKeyId: string, accountId: string): Promise<void> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 5000) {
    const result = await getEnvelope<UsageRecordListResult>(baseUrl, '/__aisys__/api/usage-records?page=1&pageSize=20&result=failed', cookie)
    if (result.items.some((record) => (
      record.apiKeyId === apiKeyId
      && record.accountId === accountId
      && record.success === false
      && record.errorMessage?.includes('代理不存在或已停用')
    ))) {
      return
    }
    await sleep(100)
  }
  throw new Error('未找到停用代理网关失败使用记录')
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

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

main().catch((error) => {
  console.error('\n代理负向回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
