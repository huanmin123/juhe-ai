import { strict as assert } from 'node:assert'
import { mkdtempSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

const inputURL = requiredEnv('JUHE_AI_OPERATION_LOG_INPUT_URL')
requiredEnv('JUHE_AI_OPERATION_LOG_INPUT_SECRET')
const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-f4-oauth-producer-'))

process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_PROCESS_ROLE = 'db-service'
process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
process.env.JUHE_AI_CACHE_DRIVER = 'memory'
process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
process.env.JUHE_AI_QUEUE_DRIVER = 'memory'
process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-context')
process.env.JUHE_AI_SECRET = 'f4-oauth-producer-smoke-secret'
process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'
process.env.JUHE_AI_LOG_FILE_ENABLED = 'false'
process.env.JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE = '4'
process.env.NODE_ENV = 'test'

let server: http.Server | undefined
let closeStorageDatabases: (() => void) | undefined
let closeSqliteReadWorkerPool: (() => Promise<void>) | undefined
let closeMock: (() => Promise<void>) | undefined
let tokenTransportCalls = 0

try {
  const [
    { createSystemApiApp },
    { captchaAnswerForTest },
    { logger },
    databaseModule,
    readWorkerPool,
    { setProviderOAuthTokenTransportForTest },
    { startProviderOAuthMockUpstream }
  ] = await Promise.all([
    import('../../modules/system-api/system-api-app.js'),
    import('../../modules/auth/captcha.service.js'),
    import('../../shared/logger.js'),
    import('../../storage/database.js'),
    import('../../storage/sqlite-read-worker-pool.js'),
    import('../../modules/providers/drivers/_shared/provider-oauth-token-transport.js'),
    import('./support/provider-oauth-mock-upstream.js')
  ])
  logger.level = 'silent'
  closeStorageDatabases = databaseModule.closeStorageDatabases
  closeSqliteReadWorkerPool = readWorkerPool.closeSqliteReadWorkerPool
  const mock = await startProviderOAuthMockUpstream()
  closeMock = mock.close
  mock.registerGeminiOAuthClient('f4-oauth-smoke-gemini-client', 'f4-oauth-smoke-gemini-secret')

  setProviderOAuthTokenTransportForTest(async (input) => {
    tokenTransportCalls += 1
    return await mock.tokenTransport(input)
  })
  try {
    databaseModule.getBusinessDatabase()
    databaseModule.getDatasetDatabase()
    databaseModule.getUsageCatalogDatabase()
    databaseModule.getStatsDatabase()

    const app = createSystemApiApp({
      systemApiPrefix: '/__aisys__/api',
      trustProxy: true,
      bypassSystemApiRateLimitForTest: true
    })
    server = app.listen(0, '127.0.0.1')
    await listen(server)
    const baseURL = `http://127.0.0.1:${addressPort(server)}`
    const cookie = await login(baseURL, captchaAnswerForTest)

    await createOAuthAccount({
      route: 'openai',
      providerCode: 'gpt',
      providerProtocolProfileId: 'profile_gpt_openai_v1',
      name: 'F4 OpenAI OAuth producer smoke',
      operationModule: 'openai_oauth',
      operationKey: 'openai_oauth.create_from_code',
      summary: '通过授权码创建 OpenAI OAuth 账户：F4 OpenAI OAuth producer smoke'
    })
    await createOAuthAccount({
      route: 'anthropic',
      providerCode: 'anthropic',
      providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
      name: 'F4 Anthropic OAuth producer smoke',
      operationModule: 'anthropic_oauth',
      operationKey: 'anthropic_oauth.create_from_code',
      summary: '通过授权码创建 Anthropic OAuth 账户：F4 Anthropic OAuth producer smoke'
    })
    await createOAuthAccount({
      route: 'gemini',
      providerCode: 'gemini',
      providerProtocolProfileId: 'profile_gemini_native_v1beta',
      name: 'F4 Gemini OAuth producer smoke',
      operationModule: 'gemini_oauth',
      operationKey: 'gemini_oauth.create_from_code',
      summary: '通过授权码创建 Gemini OAuth 账户：F4 Gemini OAuth producer smoke',
      oauthFields: {
        oauthType: 'ai_studio',
        clientId: 'f4-oauth-smoke-gemini-client',
        clientSecret: 'f4-oauth-smoke-gemini-secret'
      }
    })
    await createOAuthAccount({
      route: 'grok',
      providerCode: 'xai',
      providerProtocolProfileId: 'profile_xai_openai_v1',
      name: 'F4 Grok OAuth producer smoke',
      operationModule: 'grok_oauth',
      operationKey: 'grok_oauth.create_from_code',
      summary: '通过授权码创建 Grok OAuth 账户：F4 Grok OAuth producer smoke'
    })

    console.log(`F4 OAuth producer smoke passed: openai-oauth, anthropic-oauth, gemini-oauth, grok-oauth (${inputURL})`)

    async function createOAuthAccount(input: {
      route: 'openai' | 'anthropic' | 'gemini' | 'grok'
      providerCode: string
      providerProtocolProfileId: string
      name: string
      operationModule: string
      operationKey: string
      summary: string
      oauthFields?: Record<string, unknown>
    }): Promise<void> {
      const group = await request(baseURL, '/__aisys__/api/groups', cookie, {
        method: 'POST',
        body: { name: `${input.name} group`, providerCode: input.providerCode, enabled: true }
      })
      assert.equal(group.status, 201, `${input.route} OAuth 测试分组创建应成功：${group.text}`)
      const groupID = envelope<{ id: string }>(group.text).id
      const oauthFields = input.oauthFields ?? {}
      const auth = await request(baseURL, `/__aisys__/api/${input.route}-oauth/auth-url`, cookie, {
        method: 'POST',
        body: oauthFields
      })
      assert.equal(auth.status, 200, `${input.route} OAuth 授权 URL 应成功：${auth.text}`)
      const authorization = await mock.authorize(input.route, envelope<{ authUrl: string }>(auth.text).authUrl)
      const tokenTransportCallsBefore = tokenTransportCalls
      const created = await request(baseURL, `/__aisys__/api/${input.route}-oauth/create-from-code`, cookie, {
        method: 'POST',
        body: {
          sessionId: envelope<{ sessionId: string }>(auth.text).sessionId,
          callbackUrl: input.route === 'grok' ? authorization.callbackUrl : `${authorization.code}#${authorization.state}`,
          providerProtocolProfileId: input.providerProtocolProfileId,
          name: input.name,
          groupId: groupID,
          accountExpiresAt: null,
          availabilitySchedule: null,
          temporaryUnavailableContinuousProbeEnabled: false,
          ...oauthFields
        }
      })
      assert.equal(created.status, 201, `${input.route} OAuth 真实建号应成功：${created.text}`)
      assert.equal(tokenTransportCalls, tokenTransportCallsBefore + 1, `${input.route} OAuth 建号必须完成一次令牌交换`)
      const accountID = envelope<{ id: string }>(created.text).id
      const item = await assertOperation(baseURL, cookie, input.operationModule, input.summary)
      const detail = await request(baseURL, `/__aisys__/api/operation-logs/${encodeURIComponent(item.id)}`, cookie)
      assert.equal(detail.status, 200, `${input.route} OAuth Go owner 管理详情应成功：${detail.text}`)
      const detailData = envelope<{ operationKey?: string; resourceType?: string; path?: string }>(detail.text)
      assert.equal(detailData.operationKey, input.operationKey)
      assert.equal(detailData.resourceType, 'account')
      assert.equal(detailData.path, `/__aisys__/api/${input.route}-oauth/create-from-code`)
      const personal = await request(baseURL, `/__aisys__/api/my-operation-logs/${encodeURIComponent(item.id)}`, cookie)
      assert.equal(personal.status, 200, `${input.route} OAuth 个人详情应可读取：${personal.text}`)
      const personalData = envelope<{ changes?: unknown[]; clientIp?: unknown }>(personal.text)
      assert((personalData.changes?.length ?? 0) > 0, `${input.route} OAuth 个人详情必须保留脱敏变更`)
      assert.equal(personalData.clientIp, undefined, `${input.route} OAuth 个人详情不得暴露 clientIp`)
      assert(accountID, `${input.route} OAuth 创建响应必须返回账户 ID`)
    }
  } finally {
    setProviderOAuthTokenTransportForTest()
  }
} finally {
  if (server) await close(server)
  await closeSqliteReadWorkerPool?.().catch(() => undefined)
  closeStorageDatabases?.()
  await closeMock?.().catch(() => undefined)
  await removeTempRoot()
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`缺少 ${name}`)
  return value
}

async function login(baseURL: string, captchaAnswerForTest: (captchaId: string) => string | undefined): Promise<string> {
  const captcha = await request(baseURL, '/__aisys__/api/auth/captcha')
  assert.equal(captcha.status, 200, `OAuth smoke captcha 读取应成功：${captcha.text}`)
  const captchaID = envelope<{ captchaId: string }>(captcha.text).captchaId
  const captchaCode = captchaAnswerForTest(captchaID)
  assert(captchaCode, 'OAuth smoke 必须能取得 captcha 答案')
  const response = await request(baseURL, '/__aisys__/api/auth/login', undefined, {
    method: 'POST',
    body: { username: 'admin', password: 'admin', captchaId: captchaID, captchaCode }
  })
  assert.equal(response.status, 200, `OAuth smoke 管理员登录应成功：${response.text}`)
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert(cookie, 'OAuth smoke 登录必须返回 session cookie')
  return cookie
}

async function request(baseURL: string, path: string, cookie?: string, options: { method?: string; body?: unknown } = {}): Promise<{ status: number; text: string; headers: Headers }> {
  const response = await fetch(`${baseURL}${path}`, {
    method: options.method,
    headers: {
      ...(cookie ? { cookie } : {}),
      ...(options.body === undefined ? {} : { 'content-type': 'application/json' })
    },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    signal: AbortSignal.timeout(10_000)
  })
  return { status: response.status, text: await response.text(), headers: response.headers }
}

async function assertOperation(baseURL: string, cookie: string, module: string, summary: string): Promise<{ id: string }> {
  return eventually(async () => {
    const response = await request(baseURL, `/__aisys__/api/operation-logs?module=${encodeURIComponent(module)}&action=create_account&page=1&pageSize=20`, cookie)
    assert.equal(response.status, 200, `OAuth Go owner 管理列表应成功：${response.text}`)
    const data = envelope<{ items?: Array<{ id: string; module: string; action: string; summary: string }> }>(response.text)
    return data.items?.find((item) => item.module === module && item.action === 'create_account' && item.summary === summary)
  }, 10_000, `真实 ${module} OAuth 操作未通过 Node -> Go F4 -> Node 管理列表读回`)
}

function envelope<T>(text: string): T {
  return (JSON.parse(text) as { data: T }).data
}

async function eventually<T>(operation: () => Promise<T | undefined>, timeoutMs: number, message: string): Promise<T> {
  const deadline = Date.now() + timeoutMs
  let lastError: unknown
  while (Date.now() < deadline) {
    try {
      const value = await operation()
      if (value !== undefined) return value
    } catch (error) {
      lastError = error
    }
    await delay(50)
  }
  throw new Error(lastError ? `${message}: ${lastError instanceof Error ? lastError.message : String(lastError)}` : message)
}

async function listen(serverInstance: http.Server): Promise<void> {
  if (serverInstance.listening) return
  await new Promise<void>((resolve, reject) => {
    serverInstance.once('listening', resolve)
    serverInstance.once('error', reject)
  })
}

function addressPort(serverInstance: http.Server): number {
  const address = serverInstance.address()
  assert(address && typeof address === 'object', 'OAuth smoke 监听地址无效')
  return address.port
}

async function close(serverInstance: http.Server): Promise<void> {
  if (!serverInstance.listening) return
  await new Promise<void>((resolve, reject) => {
    serverInstance.close((error) => error ? reject(error) : resolve())
  })
}

async function removeTempRoot(): Promise<void> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    try {
      rmSync(tempRoot, { recursive: true, force: true })
      return
    } catch (error) {
      if (attempt === 4) throw error
      await delay(100 * (attempt + 1))
    }
  }
}
