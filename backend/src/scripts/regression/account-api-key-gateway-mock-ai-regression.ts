import { strict as assert } from 'node:assert'
import { spawn, type ChildProcess } from 'node:child_process'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

interface MockUpstreamHit {
  authorization: string
  path: string
}

type ApiKeyStrategy = 'round_robin' | 'weighted_round_robin'

const currentDir = dirname(fileURLToPath(import.meta.url))
const backendRoot = resolve(currentDir, '../../..')
const projectRoot = resolve(backendRoot, '..')
const useShellSpawn = process.platform === 'win32'
const tempRoot = resolve(tmpdir(), `juhe-ai-account-api-key-gateway-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'account-api-key-gateway-mock-ai-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  apiKeyRotation,
  apiKeyRuntimeRepository,
  apiKeyCooldownRetestService
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-api-key-rotation.js'),
  import('../../storage/account-api-key-runtime-state.repository.js'),
  import('../../modules/background/account-api-key-cooldown-retest.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const mockHits: MockUpstreamHit[] = []

let mockUpstream: http.Server | undefined
let backendProcess: ChildProcess | undefined
let failoverBadKeyRecovered = false

try {
  mockUpstream = createMockOpenAIUpstream()
  mockUpstream.listen(0, '127.0.0.1')
  await onceListening(mockUpstream)
  const upstreamBaseUrl = `http://127.0.0.1:${serverPort(mockUpstream)}/v1`

  const roundRobinGatewayApiKey = createGatewayApiKeyScenario({
    name: '单账户多 Key 网关轮询',
    upstreamBaseUrl,
    apiKeys: ['sk-gateway-rr-a', 'sk-gateway-rr-b', 'sk-gateway-rr-c'],
    strategy: 'round_robin'
  })
  const weightedGatewayApiKey = createGatewayApiKeyScenario({
    name: '单账户多 Key 网关权重',
    upstreamBaseUrl,
    apiKeys: ['sk-gateway-weight-a', 'sk-gateway-weight-b'],
    strategy: 'weighted_round_robin',
    weights: [3, 1]
  })
  const failoverGatewayApiKey = createGatewayApiKeyFailoverScenario(upstreamBaseUrl)
  assert.equal(
    repositories.listAccounts(access, { page: 1, pageSize: 20 }).filter((account) => account.name.includes('单账户多 Key 网关')).length,
    2,
    '两个策略场景各自只应创建一个账户，不应按 API Key 展开账户'
  )
  const admin = repositories.createSystemAccount({
    username: `api_key_gateway_mock_admin_${Date.now()}`.replace(/[^a-zA-Z0-9_]/g, '_'),
    displayName: 'APIKey网关Mock管理员',
    password: 'password',
    role: 'admin',
    status: 'active',
    mustChangePassword: false
  })
  const session = repositories.createSession(admin.id, 1)
  const cookie = `juhe_ai_session=${session.token}`
  databaseModule.closeStorageDatabases()

  const backendPort = await freePort()
  const backendBaseUrl = `http://127.0.0.1:${backendPort}`
  backendProcess = startBackendServer(backendPort)
  await waitForHealth(backendBaseUrl, backendProcess)
  await waitForApiReady(backendBaseUrl, cookie, backendProcess)

  await postChatCompletions(backendBaseUrl, roundRobinGatewayApiKey, 5)
  const roundRobinAuthorizations = lastAuthorizations(5)
  assert.deepEqual(
    roundRobinAuthorizations,
    [
      'Bearer sk-gateway-rr-a',
      'Bearer sk-gateway-rr-b',
      'Bearer sk-gateway-rr-c',
      'Bearer sk-gateway-rr-a',
      'Bearer sk-gateway-rr-b'
    ],
    '网关真实请求应在单个账户内按 API Key 轮询转发'
  )

  await postChatCompletions(backendBaseUrl, weightedGatewayApiKey, 8)
  const weightedAuthorizations = lastAuthorizations(8)
  assert.equal(
    weightedAuthorizations.filter((authorization) => authorization === 'Bearer sk-gateway-weight-a').length,
    6,
    '权重 3 的 API Key 在 8 次真实网关请求中应命中 6 次'
  )
  assert.equal(
    weightedAuthorizations.filter((authorization) => authorization === 'Bearer sk-gateway-weight-b').length,
    2,
    '权重 1 的 API Key 在 8 次真实网关请求中应命中 2 次'
  )

  const failoverStartHitCount = mockHits.length
  await postChatCompletions(backendBaseUrl, failoverGatewayApiKey, 1)
  const firstFailoverAuthorizations = mockHits.slice(failoverStartHitCount).map((hit) => hit.authorization)
  assert.deepEqual(
    firstFailoverAuthorizations,
    ['Bearer sk-gateway-failover-bad', 'Bearer sk-gateway-failover-rescue'],
    '多 Key 账户当前 Key 失败后，本次请求应切到后续账户，不应继续尝试同账户其他 Key'
  )
  await waitForApiKeyRuntimeState('sk-gateway-failover-bad', 'temporary_unavailable')
  const afterFailureStateHitCount = mockHits.length
  await postChatCompletions(backendBaseUrl, failoverGatewayApiKey, 2)
  const recoveredAccountAuthorizations = mockHits.slice(afterFailureStateHitCount).map((hit) => hit.authorization)
  assert.deepEqual(
    recoveredAccountAuthorizations,
    ['Bearer sk-gateway-failover-good', 'Bearer sk-gateway-failover-good'],
    '坏 Key 摘除后，后续请求回到同一账户时应持续使用剩余可用 Key'
  )
  assert.equal(
    mockHits.filter((hit) => hit.authorization === 'Bearer sk-gateway-failover-bad').length,
    1,
    '坏 Key 被摘除后不应在后续请求中再次被调度'
  )
  failoverBadKeyRecovered = true
  makeApiKeyRuntimeStateDueForProbe('sk-gateway-failover-bad')
  const probeCandidate = apiKeyRuntimeRepository.listAccountApiKeyRuntimeStatesDueForProbe(10)
    .find((candidate) => candidate.keyFingerprint === apiKeyRotation.fingerprintAccountApiKey('sk-gateway-failover-bad'))
  assert(probeCandidate, '坏 Key 到期后应进入 Key 级后台复测候选')
  assert(apiKeyCooldownRetestService.enqueueAccountApiKeyCooldownRetest(probeCandidate, { maxRecoveryHours: 24 }), 'Key 级后台复测候选应成功入队')
  await waitForApiKeyRuntimeState('sk-gateway-failover-bad', 'active')
  assert.equal(mockHits.length, 18, '真实 mock 上游应收到 18 次网关请求')

  console.log(JSON.stringify({
    message: '单账户多 API Key 网关 mock AI 回归通过',
    backendBaseUrl,
    mockUpstreamBaseUrl: upstreamBaseUrl,
    roundRobin: roundRobinAuthorizations,
    weighted: weightedAuthorizations,
    failover: {
      first: firstFailoverAuthorizations,
      afterKeyIsolation: recoveredAccountAuthorizations,
      backgroundRetest: 'active'
    }
  }, null, 2))
} finally {
  await stopBackendServer(backendProcess)
  await closeServer(mockUpstream)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  await removeTempRoot(tempRoot)
}

function createGatewayApiKeyScenario(input: {
  apiKeys: string[]
  name: string
  strategy: ApiKeyStrategy
  upstreamBaseUrl: string
  weights?: number[]
}): string {
  const group = repositories.createGroup({
    name: `${input.name} 分组`,
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    name: `${input.name} 账户`,
    type: 'api_key',
    credentials: {
      api_key: input.apiKeys[0],
      api_keys: input.apiKeys,
      api_key_strategy: input.strategy,
      api_key_weights: input.weights,
      base_url: input.upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)
  assert.deepEqual(account.credentials.api_keys, input.apiKeys, `${input.name} 应把多个 API Key 保存在同一个账户`)
  const apiKey = repositories.createApiKeyRecord({
    name: `${input.name} 网关 Key`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, `${input.name} 未返回网关 API Key 明文`)
  return apiKey.key
}

function createGatewayApiKeyFailoverScenario(upstreamBaseUrl: string): string {
  const group = repositories.createGroup({
    name: '多 Key 摘除切号分组',
    providerCode: GPT_VENDOR_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    name: 'A 多 Key 摘除来源账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-failover-bad',
      api_keys: ['sk-gateway-failover-bad', 'sk-gateway-failover-good'],
      api_key_strategy: 'round_robin',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0
  }, access)
  repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    name: 'B 多 Key 摘除救援账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-gateway-failover-rescue',
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 10
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '多 Key 摘除切号网关 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '多 Key 摘除切号场景未返回网关 API Key 明文')
  return apiKey.key
}

async function postChatCompletions(backendBaseUrl: string, apiKey: string, count: number): Promise<void> {
  for (let index = 0; index < count; index += 1) {
    const response = await fetch(`${backendBaseUrl}/v1/chat/completions`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-5.5',
        messages: [{ role: 'user', content: `mock gateway api key rotation ${index + 1}` }],
        stream: false
      })
    })
    const text = await response.text()
    assert.equal(response.status, 200, `网关请求应成功，实际 HTTP ${response.status}: ${text}`)
  }
}

function lastAuthorizations(count: number): string[] {
  return mockHits.slice(-count).map((hit) => hit.authorization)
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const requestPath = req.url ?? ''
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      if (req.method !== 'POST' || (requestPath !== '/v1/chat/completions' && requestPath !== '/v1/responses')) {
        sendJsonError(res, 404, 'mock upstream path not found')
        return
      }
      mockHits.push({
        authorization: String(req.headers.authorization ?? ''),
        path: requestPath
      })
      if (req.headers.authorization === 'Bearer sk-gateway-failover-bad' && !failoverBadKeyRecovered) {
        sendJsonError(res, 503, 'mock failover key unavailable')
        return
      }
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify(successPayloadForPath(requestPath)))
    })
  })
}

function successPayloadForPath(requestPath: string): Record<string, unknown> {
  if (requestPath === '/v1/responses') {
    return {
      id: 'resp-account-api-key-gateway-mock-ai',
      object: 'response',
      output: [
        {
          type: 'message',
          content: [{ type: 'output_text', text: 'OK' }]
        }
      ],
      usage: {
        input_tokens: 3,
        output_tokens: 4,
        total_tokens: 7
      }
    }
  }
  return {
    id: 'chatcmpl-account-api-key-gateway-mock-ai',
    object: 'chat.completion',
    choices: [
      {
        index: 0,
        message: { role: 'assistant', content: 'mock api key gateway ok' },
        finish_reason: 'stop'
      }
    ],
    usage: {
      input_tokens: 3,
      output_tokens: 4,
      total_tokens: 7
    }
  }
}

async function waitForApiKeyRuntimeState(key: string, status: string): Promise<void> {
  const fingerprint = apiKeyRotation.fingerprintAccountApiKey(key)
  const startedAt = Date.now()
  let lastStatus: string | undefined
  while (Date.now() - startedAt < 5000) {
    const row = databaseModule.getBusinessDatabase()
      .prepare('SELECT status FROM account_api_key_runtime_states WHERE key_fingerprint = ? LIMIT 1')
      .get(fingerprint) as unknown as { status?: string } | undefined
    lastStatus = row?.status
    if (lastStatus === status) {
      return
    }
    await sleep(100)
  }
  throw new Error(`等待 API Key 运行态超时：期望 ${status}，实际 ${lastStatus ?? 'missing'}`)
}

function makeApiKeyRuntimeStateDueForProbe(key: string): void {
  const fingerprint = apiKeyRotation.fingerprintAccountApiKey(key)
  const dueAt = new Date(Date.now() - 1000).toISOString()
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE account_api_key_runtime_states SET next_probe_at = ?, cooldown_until = ?, updated_at = ? WHERE key_fingerprint = ?')
    .run(dueAt, dueAt, dueAt, fingerprint)
}

function sendJsonError(res: http.ServerResponse, statusCode: number, message: string): void {
  res.writeHead(statusCode, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({ error: { message, code: `mock_${statusCode}` } }))
}

function startBackendServer(port: number): ChildProcess {
  const child = spawn('pnpm', ['--filter', 'juhe-ai-backend', 'exec', 'tsx', 'src/server.ts'], {
    cwd: projectRoot,
    env: {
      ...process.env,
      NODE_ENV: '',
      JUHE_AI_HOST: '127.0.0.1',
      JUHE_AI_PORT: String(port),
      JUHE_AI_DB_SERVICE_HTTP_HOST: '127.0.0.1',
      JUHE_AI_DB_SERVICE_HTTP_PORT: '0',
      JUHE_AI_DATABASE_PATH: runtimeConfig.databasePath,
      JUHE_AI_DATASET_DATABASE_PATH: runtimeConfig.datasetDatabasePath,
      JUHE_AI_STATS_DATABASE_PATH: runtimeConfig.statsDatabasePath,
      JUHE_AI_USAGE_SHARD_ROOT: runtimeConfig.usageShardRoot,
      JUHE_AI_SECRET: runtimeConfig.secret,
      JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS: 'true',
      JUHE_AI_LOG_LEVEL: 'warn',
      JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
      JUHE_AI_LOG_FILE_ENABLED: 'false'
    },
    shell: useShellSpawn,
    stdio: ['ignore', 'pipe', 'pipe']
  })
  child.stdout?.on('data', (chunk) => {
    process.stdout.write(`[api-key-gateway-backend] ${String(chunk)}`)
  })
  child.stderr?.on('data', (chunk) => {
    process.stderr.write(`[api-key-gateway-backend] ${String(chunk)}`)
  })
  return child
}

async function waitForHealth(baseUrl: string, child: ChildProcess): Promise<void> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 20_000) {
    if (child.exitCode !== null) {
      throw new Error(`临时后端提前退出，exitCode=${child.exitCode}`)
    }
    try {
      const response = await fetch(`${baseUrl}/__aisys__/health`)
      if (response.ok) return
    } catch {
    }
    await sleep(200)
  }
  throw new Error('临时后端健康检查等待超时')
}

async function waitForApiReady(baseUrl: string, cookie: string, child: ChildProcess): Promise<void> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 20_000) {
    if (child.exitCode !== null) {
      throw new Error(`临时后端提前退出，exitCode=${child.exitCode}`)
    }
    try {
      const response = await fetch(`${baseUrl}/__aisys__/api/auth/me`, { headers: { cookie } })
      if (response.ok) return
    } catch {
    }
    await sleep(200)
  }
  throw new Error('临时后端管理 API 等待超时')
}

async function onceListening(server: http.Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

async function closeServer(server?: http.Server): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) {
        rejectPromise(error)
        return
      }
      resolvePromise()
    })
  })
}

async function stopBackendServer(child?: ChildProcess): Promise<void> {
  if (!child || child.exitCode !== null) return
  if (process.platform === 'win32' && child.pid) {
    await killWindowsProcessTree(child.pid)
  } else {
    child.kill('SIGTERM')
  }
  await new Promise<void>((resolvePromise) => {
    const timeout = setTimeout(() => {
      child.kill('SIGKILL')
      resolvePromise()
    }, 3000)
    child.once('exit', () => {
      clearTimeout(timeout)
      resolvePromise()
    })
  })
  await sleep(500)
}

async function killWindowsProcessTree(pid: number): Promise<void> {
  await new Promise<void>((resolvePromise) => {
    const killer = spawn('taskkill', ['/pid', String(pid), '/t', '/f'], { stdio: 'ignore' })
    killer.once('error', () => resolvePromise())
    killer.once('exit', () => resolvePromise())
  })
}

async function removeTempRoot(path: string): Promise<void> {
  let lastError: unknown
  for (let attempt = 0; attempt < 10; attempt += 1) {
    try {
      rmSync(path, { recursive: true, force: true })
      return
    } catch (error) {
      lastError = error
      await sleep(200)
    }
  }
  throw lastError
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(address && typeof address !== 'string', 'mock AI upstream should be listening')
  return address.port
}

async function freePort(): Promise<number> {
  const server = net.createServer()
  server.listen(0, '127.0.0.1')
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
  const address = server.address()
  assert(address && typeof address !== 'string', 'free port server should be listening')
  const port = address.port
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) {
        rejectPromise(error)
        return
      }
      resolvePromise()
    })
  })
  return port
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}
