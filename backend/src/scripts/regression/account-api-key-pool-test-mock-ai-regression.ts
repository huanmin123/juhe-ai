import { strict as assert } from 'node:assert'
import { spawn, type ChildProcess } from 'node:child_process'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'

const currentDir = dirname(fileURLToPath(import.meta.url))
const backendRoot = resolve(currentDir, '../../..')
const projectRoot = resolve(backendRoot, '..')
const useShellSpawn = process.platform === 'win32'
const tempRoot = resolve(tmpdir(), `juhe-ai-account-api-key-pool-test-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const taskMaxWaitMs = 60_000
const pollIntervalMs = 100

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'account-api-key-pool-test-mock-ai-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

interface ApiEnvelope<T> {
  data?: T
  message?: string
}

interface AccountTestTask {
  id: string
  status: 'queued' | 'running' | 'success' | 'failed' | 'canceled'
  message?: string
  result?: AccountTestResult
}

interface TestContext {
  backendBaseUrl: string
  cookie: string
  groupId: string
  mockBaseUrl: string
}

type AccountTestEndpointMode = 'chat_sse' | 'responses_sse'

const mockState = {
  hitsByKey: new Map<string, number>()
}

const [
  databaseModule,
  repositories
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

let mockUpstream: http.Server | undefined
let backendProcess: ChildProcess | undefined

try {
  repositories.updateSettings({
    systemApiRateLimitIpReadPerMinute: 1_000_000,
    systemApiRateLimitIpReadBurstPer10Seconds: 1_000_000,
    systemApiRateLimitIpWritePerMinute: 1_000_000,
    systemApiRateLimitIpWriteBurstPer10Seconds: 1_000_000,
    systemApiRateLimitUserReadPerMinute: 1_000_000,
    systemApiRateLimitUserWritePerMinute: 1_000_000
  })
  mockUpstream = createMockAIUpstream()
  mockUpstream.listen(0, '127.0.0.1')
  await onceListening(mockUpstream)
  const mockBaseUrl = `http://127.0.0.1:${serverPort(mockUpstream)}`

  const backendPort = await freePort()
  const backendBaseUrl = `http://127.0.0.1:${backendPort}`
  const admin = repositories.createSystemAccount({
    username: `api_key_pool_test_admin_${Date.now()}`.replace(/[^a-zA-Z0-9_]/g, '_'),
    displayName: 'Key池批测Mock管理员',
    password: 'password',
    role: 'admin',
    status: 'active',
    mustChangePassword: false
  })
  const session = repositories.createSession(admin.id, 1)
  const cookie = `juhe_ai_session=${session.token}`
  prepareMainDatabaseSchemasForChildServer()
  databaseModule.closeStorageDatabases()

  backendProcess = startBackendServer(backendPort)
  await waitForHealth(backendBaseUrl, backendProcess)
  await waitForApiReady(backendBaseUrl, cookie, backendProcess)

  const group = await postEnvelope<{ id: string; name: string }>(backendBaseUrl, '/groups', cookie, {
    name: `Key池批测 mock 分组 ${Date.now()}`,
    providerCode: 'gpt'
  })
  const context: TestContext = {
    backendBaseUrl,
    cookie,
    groupId: group.id,
    mockBaseUrl
  }

  const savedAccount = await postEnvelope<AccountSummary>(backendBaseUrl, '/accounts', cookie, accountPayload({
    name: '已保存 Key 池批测账户',
    apiKeys: ['sk-pool-saved-bad-a', 'sk-pool-saved-good', 'sk-pool-saved-bad-b'],
    groupId: group.id,
    mockBaseUrl
  }))
  assert.equal(savedAccount.status, 'pending_test', '未携带激活测试任务创建的 Key 池账户应先进入待测试')
  const savedTask = await submitAccountTest(context, savedAccount.id)
  const savedFinished = await waitForTask(context, savedTask.id)
  assert.equal(savedFinished.status, 'success', '已保存 Key 池账户只要至少一个 Key 可用就应测试成功')
  assert.equal(savedFinished.result?.apiKeyPool?.total, 3, '已保存账户测试应返回 Key 池总数')
  assert.equal(savedFinished.result?.apiKeyPool?.successCount, 1, '已保存账户测试应统计 1 个可用 Key')
  assert.equal(savedFinished.result?.apiKeyPool?.failedCount, 2, '已保存账户测试应统计 2 个不可用 Key')
  assertKeyPoolPreview(savedFinished.result, 1, 'sk-p', 'good', '已保存账户可用 Key 应返回安全预览')
  const savedDetail = await waitForAccountRuntimeDetails(context, savedAccount.id, 3)
  assertKeyRuntime(savedDetail, 'good', 'active', '已保存账户可用 Key 应写入 active 运行态')
  assertKeyRuntime(savedDetail, 'ad-a', 'temporary_unavailable', '已保存账户坏 Key A 应进入后台恢复')
  assertKeyRuntime(savedDetail, 'ad-b', 'temporary_unavailable', '已保存账户坏 Key B 应进入后台恢复')

  const responsesAccount = await postEnvelope<AccountSummary>(backendBaseUrl, '/accounts', cookie, accountPayload({
    name: '已保存 Key 池 Responses 批测账户',
    apiKeys: ['sk-pool-responses-bad', 'sk-pool-responses-good'],
    groupId: group.id,
    mockBaseUrl
  }))
  const responsesTask = await submitAccountTest(context, responsesAccount.id, 'responses_sse')
  const responsesFinished = await waitForTask(context, responsesTask.id)
  assert.equal(responsesFinished.status, 'success', 'Responses SSE Key 池账户只要至少一个 Key 可用就应测试成功')
  assert.equal(responsesFinished.result?.testEndpointMode, 'responses_sse', 'Responses SSE Key 池测试应记录实际测试接口形态')
  assert.equal(responsesFinished.result?.apiKeyPool?.successCount, 1, 'Responses SSE 测试应统计 1 个可用 Key')
  assert.equal(responsesFinished.result?.apiKeyPool?.failedCount, 1, 'Responses SSE 测试应统计 1 个不可用 Key')
  assertKeyPoolPreview(responsesFinished.result, 1, 'sk-p', 'good', 'Responses SSE 可用 Key 应返回安全预览')

  const draftAccount = accountPayload({
    name: '创建 Key 池批测账户',
    apiKeys: ['sk-pool-create-bad-a', 'sk-pool-create-good', 'sk-pool-create-bad-b'],
    groupId: group.id,
    mockBaseUrl
  })
  const draftTask = await submitDraftAccountTest(context, draftAccount)
  const draftFinished = await waitForTask(context, draftTask.id)
  assert.equal(draftFinished.status, 'success', '创建草稿 Key 池只要至少一个 Key 可用就应测试成功')
  assert.equal(draftFinished.result?.apiKeyPool?.successCount, 1, '创建草稿测试应统计 1 个可用 Key')
  assert.equal(draftFinished.result?.apiKeyPool?.failedCount, 2, '创建草稿测试应统计 2 个不可用 Key')
  assertKeyPoolPreview(draftFinished.result, 1, 'sk-p', 'good', '创建草稿可用 Key 应返回安全预览')

  const createdAccount = await postEnvelope<AccountSummary>(backendBaseUrl, '/accounts', cookie, {
    ...draftAccount,
    activationTestTaskId: draftTask.id
  })
  assert.equal(createdAccount.status, 'active', 'Key 池草稿测试成功后保存账户应直接启用')
  const createdDetail = await waitForAccountRuntimeDetails(context, createdAccount.id, 3)
  assertKeyRuntime(createdDetail, 'good', 'active', '创建账户可用 Key 应在保存后写入 active 运行态')
  assertKeyRuntime(createdDetail, 'ad-a', 'temporary_unavailable', '创建账户坏 Key A 应在保存后进入后台恢复')
  assertKeyRuntime(createdDetail, 'ad-b', 'temporary_unavailable', '创建账户坏 Key B 应在保存后进入后台恢复')

  assert.equal(mockState.hitsByKey.get('pool-saved-bad-a'), 1, '已保存账户坏 Key A 应被测试一次')
  assert.equal(mockState.hitsByKey.get('pool-saved-good'), 1, '已保存账户好 Key 应被测试一次')
  assert.equal(mockState.hitsByKey.get('pool-saved-bad-b'), 1, '已保存账户坏 Key B 应被测试一次')
  assert.equal(mockState.hitsByKey.get('pool-responses-bad'), 1, 'Responses SSE 坏 Key 应被测试一次')
  assert.equal(mockState.hitsByKey.get('pool-responses-good'), 1, 'Responses SSE 好 Key 应被测试一次')
  assert.equal(mockState.hitsByKey.get('pool-create-bad-a'), 1, '创建草稿坏 Key A 应被测试一次')
  assert.equal(mockState.hitsByKey.get('pool-create-good'), 1, '创建草稿好 Key 应被测试一次')
  assert.equal(mockState.hitsByKey.get('pool-create-bad-b'), 1, '创建草稿坏 Key B 应被测试一次')

  console.log(JSON.stringify({
    message: '账户内 API Key 池测试 mock AI 回归通过',
    backendBaseUrl,
    mockBaseUrl,
    saved: savedFinished.result?.apiKeyPool,
    responses: responsesFinished.result?.apiKeyPool,
    created: draftFinished.result?.apiKeyPool
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

function accountPayload(input: {
  name: string
  apiKeys: string[]
  groupId: string
  mockBaseUrl: string
}) {
  return {
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: input.name,
    type: 'api_key',
    credentials: {
      api_key: input.apiKeys[0],
      api_keys: input.apiKeys,
      api_key_strategy: 'round_robin',
      base_url: input.mockBaseUrl
    },
    groupId: input.groupId,
    concurrencyLimit: 20,
    priority: 0,
    supportedModels: ['gpt-5.5']
  }
}

async function submitAccountTest(context: TestContext, accountId: string, testEndpointMode?: AccountTestEndpointMode): Promise<AccountTestTask> {
  return await postEnvelope<AccountTestTask>(context.backendBaseUrl, `/accounts/${accountId}/test`, context.cookie, {
    model: 'gpt-5.5',
    ...(testEndpointMode ? { testEndpointMode } : {})
  })
}

async function submitDraftAccountTest(context: TestContext, account: ReturnType<typeof accountPayload>, testEndpointMode?: AccountTestEndpointMode): Promise<AccountTestTask> {
  return await postEnvelope<AccountTestTask>(context.backendBaseUrl, '/accounts/test-draft', context.cookie, {
    account,
    model: 'gpt-5.5',
    ...(testEndpointMode ? { testEndpointMode } : {})
  })
}

async function waitForTask(context: TestContext, taskId: string): Promise<AccountTestTask> {
  const startedAt = Date.now()
  let latest = await getTask(context, taskId)
  while (Date.now() - startedAt <= taskMaxWaitMs) {
    if (latest.status === 'success' || latest.status === 'failed') {
      assert(latest.result, `任务 ${taskId} 已结束但没有结果`)
      return latest
    }
    if (latest.status === 'canceled') {
      throw new Error(`任务 ${taskId} 被取消：${latest.message ?? ''}`)
    }
    await sleep(pollIntervalMs)
    latest = await getTask(context, taskId)
  }
  throw new Error(`任务 ${taskId} 超过 ${taskMaxWaitMs}ms 仍未结束，最后状态：${latest.status} ${latest.message ?? ''}`)
}

async function getTask(context: TestContext, taskId: string): Promise<AccountTestTask> {
  const tasks = await getEnvelope<AccountTestTask[]>(context.backendBaseUrl, `/accounts/test-tasks?ids=${encodeURIComponent(taskId)}`, context.cookie)
  const task = tasks.find((item) => item.id === taskId)
  assert(task, `任务 ${taskId} 应可查询`)
  return task
}

async function waitForAccountRuntimeDetails(context: TestContext, accountId: string, expectedCount: number): Promise<AccountSummary> {
  let latest = await getAccount(context, accountId)
  for (let attempt = 0; attempt < 50; attempt += 1) {
    if ((latest.apiKeyRuntimeDetails?.length ?? 0) >= expectedCount) {
      return latest
    }
    await sleep(100)
    latest = await getAccount(context, accountId)
  }
  return latest
}

async function getAccount(context: TestContext, accountId: string): Promise<AccountSummary> {
  return await getEnvelope<AccountSummary>(context.backendBaseUrl, `/accounts/${accountId}/advanced`, context.cookie)
}

function assertKeyRuntime(account: AccountSummary, suffix: string, status: string, message: string): void {
  const detail = account.apiKeyRuntimeDetails?.find((item) => item.keySuffix === suffix)
  assert(detail, `${message}：缺少尾号 ${suffix} 的 Key 运行态`)
  assert.equal(detail.status, status, message)
}

function assertKeyPoolPreview(result: AccountTestResult | undefined, keyIndex: number, prefix: string, suffix: string, message: string): void {
  const detail = result?.apiKeyPool?.results.find((item) => item.keyIndex === keyIndex)
  assert(detail, `${message}：缺少序号 ${keyIndex} 的 Key 测试结果`)
  assert.equal(detail.keyPrefix, prefix, `${message}：前缀不匹配`)
  assert.equal(detail.keySuffix, suffix, `${message}：后缀不匹配`)
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
      JUHE_AI_LOG_CONSOLE_ENABLED: process.env.JUHE_AI_REGRESSION_SERVER_LOG_CONSOLE ?? 'false',
      JUHE_AI_LOG_FILE_ENABLED: 'false'
    },
    shell: useShellSpawn,
    stdio: ['ignore', 'pipe', 'pipe']
  })
  child.stdout?.on('data', (chunk) => {
    process.stdout.write(`[api-key-pool-test-backend] ${String(chunk)}`)
  })
  child.stderr?.on('data', (chunk) => {
    process.stderr.write(`[api-key-pool-test-backend] ${String(chunk)}`)
  })
  return child
}

function prepareMainDatabaseSchemasForChildServer(): void {
  databaseModule.getDatasetDatabase()
  databaseModule.getStatsDatabase()
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

async function getEnvelope<T>(baseUrl: string, apiPath: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}/__aisys__/api${apiPath}`, { headers: { cookie } })
  return parseEnvelope<T>(apiPath, response)
}

async function postEnvelope<T>(baseUrl: string, apiPath: string, cookie: string, body: Record<string, unknown>): Promise<T> {
  const response = await fetch(`${baseUrl}/__aisys__/api${apiPath}`, {
    method: 'POST',
    headers: {
      cookie,
      'content-type': 'application/json'
    },
    body: JSON.stringify(body)
  })
  return parseEnvelope<T>(apiPath, response)
}

async function parseEnvelope<T>(apiPath: string, response: Response): Promise<T> {
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${apiPath} HTTP ${response.status}: ${text}`)
  }
  const parsed = JSON.parse(text) as ApiEnvelope<T>
  if (parsed.data === undefined) {
    throw new Error(`${apiPath} 响应缺少 data：${text}`)
  }
  return parsed.data
}

function createMockAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    req.on('data', () => {})
    req.on('end', () => {
      if (req.method !== 'POST' || (url.pathname !== '/v1/responses' && url.pathname !== '/v1/chat/completions')) {
        sendJsonError(res, 404, 'mock path not found')
        return
      }
      const key = upstreamKey(req.headers.authorization)
      mockState.hitsByKey.set(key, (mockState.hitsByKey.get(key) ?? 0) + 1)
      if (key.includes('bad')) {
        sendJsonError(res, 401, `mock invalid key ${key}`)
        return
      }
      if (url.pathname === '/v1/chat/completions') {
        sendChatCompletionsCompleted(res, `OK ${key}`)
        return
      }
      sendResponsesCompleted(res, `OK ${key}`)
    })
  })
}

function upstreamKey(authorization: string | string[] | undefined): string {
  const value = Array.isArray(authorization) ? authorization[0] : authorization
  return String(value ?? '').replace(/^Bearer\s+/i, '').replace(/^sk-/, '')
}

function sendJsonError(res: http.ServerResponse, statusCode: number, message: string): void {
  res.writeHead(statusCode, { 'content-type': 'application/json' })
  res.end(JSON.stringify({ error: { message, code: `mock_${statusCode}` } }))
}

function sendResponsesCompleted(res: http.ServerResponse, outputText: string): void {
  const completedEvent = {
    type: 'response.completed',
    response: {
      id: 'resp_account_api_key_pool_test_mock_ai',
      object: 'response',
      status: 'completed',
      output: [
        {
          type: 'message',
          content: [{ type: 'output_text', text: outputText }]
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
}

function sendChatCompletionsCompleted(res: http.ServerResponse, outputText: string): void {
  const chunk = {
    id: 'chatcmpl_account_api_key_pool_test_mock_ai',
    object: 'chat.completion.chunk',
    choices: [
      {
        index: 0,
        delta: { content: outputText },
        finish_reason: null
      }
    ]
  }
  const done = {
    id: 'chatcmpl_account_api_key_pool_test_mock_ai',
    object: 'chat.completion.chunk',
    choices: [
      {
        index: 0,
        delta: {},
        finish_reason: 'stop'
      }
    ],
    usage: {
      prompt_tokens: 1,
      completion_tokens: 1,
      total_tokens: 2
    }
  }
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.end(`data: ${JSON.stringify(chunk)}\n\ndata: ${JSON.stringify(done)}\n\ndata: [DONE]\n\n`)
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
