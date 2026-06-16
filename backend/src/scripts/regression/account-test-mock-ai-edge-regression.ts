import { strict as assert } from 'node:assert'
import { spawn, type ChildProcess } from 'node:child_process'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import type { AccountSummary, AccountTestResult, AccountTestSession } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'

const currentDir = dirname(fileURLToPath(import.meta.url))
const backendRoot = resolve(currentDir, '../../..')
const projectRoot = resolve(backendRoot, '..')
const useShellSpawn = process.platform === 'win32'
const tempRoot = resolve(tmpdir(), `juhe-ai-account-test-mock-ai-edge-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const pollIntervalMs = 200

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'account-test-mock-ai-edge-secret'
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
  queuedAt?: string
  startedAt?: string
  finishedAt?: string
}

interface TestContext {
  backendBaseUrl: string
  cookie: string
  groupId: string
  mockBaseUrl: string
}

const mockState = {
  hitsByKey: new Map<string, number>(),
  abortedByKey: new Map<string, number>()
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
  repositories.updateSettings({ accountTestTaskConcurrency: 1 })
  mockUpstream = createMockAIUpstream()
  mockUpstream.listen(0, '127.0.0.1')
  await onceListening(mockUpstream)
  const mockBaseUrl = `http://127.0.0.1:${serverPort(mockUpstream)}`
  const backendPort = await freePort()
  const backendBaseUrl = `http://127.0.0.1:${backendPort}`
  const admin = repositories.createSystemAccount({
    username: `acct_edge_admin_${Date.now()}`.replace(/[^a-zA-Z0-9_]/g, '_'),
    displayName: '账号测试边界Mock管理员',
    password: 'password',
    role: 'admin',
    status: 'active',
    mustChangePassword: false
  })
  const session = repositories.createSession(admin.id, 1)
  const cookie = `juhe_ai_session=${session.token}`
  databaseModule.closeStorageDatabases()

  backendProcess = startBackendServer(backendPort)
  await waitForHealth(backendBaseUrl, backendProcess)
  await waitForApiReady(backendBaseUrl, cookie, backendProcess)

  const group = await postEnvelope<{ id: string; name: string }>(backendBaseUrl, '/groups', cookie, {
    name: `账号测试边界 mock 分组 ${Date.now()}`,
    providerCode: 'gpt'
  })
  const context: TestContext = {
    backendBaseUrl,
    cookie,
    groupId: group.id,
    mockBaseUrl
  }

  const retryAccount = await createMockAccount(context, '重试后成功账号', 'sk-retry-once')
  const retryTask = await submitAccountTest(context, retryAccount)
  const retryFinished = await waitForTask(context, retryTask.id, 20_000, (task) => task.status === 'success' || task.status === 'failed')
  assert.equal(retryFinished.status, 'success', '10s 超时后的第二次真实请求应继续重试并成功')
  assert.equal(mockState.hitsByKey.get('retry-once'), 2, 'retry-once 账号应命中 mock AI 两次')

  const precheckRecoverAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: `失败确认恢复账号 ${Date.now()} ${Math.random().toString(16).slice(2, 6)}`,
    type: 'api_key',
    status: 'active',
    credentials: {
      api_key: 'sk-precheck-recover',
      base_url: context.mockBaseUrl
    },
    groupId: context.groupId,
    clientCompatibility: 'codex_responses'
  }, { systemAccountId: admin.id, role: 'admin' })
  databaseModule.closeStorageDatabases()
  const precheckRecoverTask = await submitAccountTest(context, precheckRecoverAccount)
  const precheckRecoverFinished = await waitForTask(context, precheckRecoverTask.id, 20_000, (task) => task.status === 'failed')
  assert.equal(precheckRecoverFinished.status, 'failed', '前三次真实请求均失败时账号测试任务应失败')
  await waitForCondition(10_000, () => (mockState.hitsByKey.get('precheck-recover') ?? 0) >= 4, '等待失败事前确认请求')
  const precheckRecoveredAccount = await getAccount(context, precheckRecoverAccount.id)
  assert.equal(precheckRecoveredAccount.status, 'active', '账号测试失败后事前确认恢复时不应把账号改为临时不可调用')

  const timeoutAccount = await createMockAccount(context, '真实超时账号', 'sk-timeout-always')
  const queuedAccount = await createMockAccount(context, '排队不计时账号', 'sk-queued-fast')
  const timeoutTask = await submitAccountTest(context, timeoutAccount)
  const queuedTask = await submitAccountTest(context, queuedAccount)
  const queuedAfterTwoSeconds = await waitForTask(context, queuedTask.id, 2_000, (task) => task.status === 'queued')
  assert.equal(queuedAfterTwoSeconds.status, 'queued', '并发为 1 时第二个任务应保持 queued')
  assert.equal(queuedAfterTwoSeconds.startedAt, undefined, 'queued 任务不应写入 startedAt')
  const timeoutFinished = await waitForTask(context, timeoutTask.id, 75_000, (task) => task.status === 'failed')
  assert.equal(timeoutFinished.status, 'failed', '持续无响应的真实请求应在 10s + 20s + 30s 后失败')
  assert.match(timeoutFinished.message ?? timeoutFinished.result?.message ?? '', /超时|aborted|失败|未完成/i, '超时任务应返回可解释的失败信息')
  const queuedFinished = await waitForTask(context, queuedTask.id, 10_000, (task) => task.status === 'success')
  assert.equal(queuedFinished.status, 'success', '前一个运行任务失败后，排队任务应继续被消费并成功')
  assert(queuedFinished.startedAt && Date.parse(queuedFinished.startedAt) > Date.parse(queuedTask.queuedAt ?? queuedFinished.startedAt), '排队任务应在被 worker 接收后才写 startedAt')

  const cancelSession = await createTestSession(context)
  const runningCancelAccount = await createMockAccount(context, '运行中取消账号', 'sk-cancel-running')
  const queuedCancelAccount = await createMockAccount(context, '排队取消账号', 'sk-cancel-queued')
  const runningCancelTask = await submitAccountTest(context, runningCancelAccount, cancelSession.id)
  const queuedCancelTask = await submitAccountTest(context, queuedCancelAccount, cancelSession.id)
  await waitForTask(context, runningCancelTask.id, 10_000, (task) => task.status === 'running')
  const queuedBeforeCancel = await getTask(context, queuedCancelTask.id)
  assert.equal(queuedBeforeCancel.status, 'queued', 'session 取消前第二个任务应仍在 queued')
  await cancelTestSession(context, cancelSession.id)
  const canceledRunning = await waitForTask(context, runningCancelTask.id, 10_000, (task) => task.status === 'canceled')
  const canceledQueued = await waitForTask(context, queuedCancelTask.id, 10_000, (task) => task.status === 'canceled')
  assert.equal(canceledRunning.status, 'canceled', 'session 取消应中断 running 任务')
  assert.equal(canceledQueued.status, 'canceled', 'session 取消应剔除 queued 任务')
  assert.equal(mockState.hitsByKey.get('cancel-queued') ?? 0, 0, '被 session 取消的 queued 任务不应再命中 mock AI')
  assert((mockState.abortedByKey.get('cancel-running') ?? 0) >= 1, 'running 任务取消应 abort 上游请求')

  const staleSession = await createTestSession(context)
  const staleAccount = await createMockAccount(context, '心跳过期账号', 'sk-stale-session')
  const staleTask = await submitAccountTest(context, staleAccount, staleSession.id)
  const staleFinished = await waitForTask(context, staleTask.id, 25_000, (task) => task.status === 'canceled')
  assert.equal(staleFinished.status, 'canceled', '没有 heartbeat 的 session 应过期取消任务')
  assert.match(staleFinished.message ?? '', /前端测试窗口已关闭|任务已取消/, 'heartbeat 过期取消应返回中文原因')

  console.log(JSON.stringify({
    message: '账号测试 mock AI 边界覆盖通过',
    backendBaseUrl,
    mockBaseUrl,
    scenarios: [
      '10s attempt 超时后重试成功',
      '测试失败后事前确认恢复不改账号状态',
      'running 60s 总超时失败',
      'queued 超过运行任务耗时不计超时且后续成功',
      'session 取消 running/queued',
      'session heartbeat 过期自动取消'
    ],
    hitsByKey: Object.fromEntries(mockState.hitsByKey),
    abortedByKey: Object.fromEntries(mockState.abortedByKey)
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

async function createMockAccount(context: TestContext, name: string, apiKey: string): Promise<AccountSummary> {
  return postEnvelope<AccountSummary>(context.backendBaseUrl, '/accounts', context.cookie, {
    providerCode: 'gpt',
    name: `${name} ${Date.now()} ${Math.random().toString(16).slice(2, 6)}`,
    type: 'api_key',
    credentials: {
      api_key: apiKey,
      base_url: context.mockBaseUrl
    },
    groupId: context.groupId,
    clientCompatibility: 'codex_responses'
  })
}

async function createTestSession(context: TestContext): Promise<AccountTestSession> {
  return postEnvelope<AccountTestSession>(context.backendBaseUrl, '/accounts/test-sessions', context.cookie, {})
}

async function getAccount(context: TestContext, accountId: string): Promise<AccountSummary> {
  return getEnvelope<AccountSummary>(context.backendBaseUrl, `/accounts/${encodeURIComponent(accountId)}`, context.cookie)
}

async function cancelTestSession(context: TestContext, sessionId: string): Promise<AccountTestSession> {
  return postEnvelope<AccountTestSession>(context.backendBaseUrl, `/accounts/test-sessions/${sessionId}/cancel`, context.cookie, {})
}

async function submitAccountTest(context: TestContext, account: AccountSummary, testSessionId?: string): Promise<AccountTestTask> {
  return postEnvelope<AccountTestTask>(context.backendBaseUrl, `/accounts/${account.id}/test`, context.cookie, {
    model: 'gpt-5.5',
    testSessionId
  })
}

async function getTask(context: TestContext, taskId: string): Promise<AccountTestTask> {
  const tasks = await getEnvelope<AccountTestTask[]>(context.backendBaseUrl, `/accounts/test-tasks?ids=${encodeURIComponent(taskId)}`, context.cookie)
  const task = tasks.find((item) => item.id === taskId)
  assert(task, `任务 ${taskId} 应可查询`)
  return task
}

async function waitForTask(
  context: TestContext,
  taskId: string,
  timeoutMs: number,
  done: (task: AccountTestTask) => boolean
): Promise<AccountTestTask> {
  const startedAt = Date.now()
  let latest = await getTask(context, taskId)
  while (Date.now() - startedAt <= timeoutMs) {
    if (done(latest)) {
      return latest
    }
    await sleep(pollIntervalMs)
    latest = await getTask(context, taskId)
  }
  throw new Error(`等待任务 ${taskId} 超时，最后状态：${latest.status} ${latest.message ?? ''}`)
}

async function waitForCondition(timeoutMs: number, done: () => boolean, message: string): Promise<void> {
  const startedAt = Date.now()
  while (Date.now() - startedAt <= timeoutMs) {
    if (done()) {
      return
    }
    await sleep(pollIntervalMs)
  }
  throw new Error(`${message} 超时`)
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
    process.stdout.write(`[edge-test-backend] ${String(chunk)}`)
  })
  child.stderr?.on('data', (chunk) => {
    process.stderr.write(`[edge-test-backend] ${String(chunk)}`)
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
      if (req.method !== 'POST' || url.pathname !== '/v1/responses') {
        sendJsonError(res, 404, 'mock path not found')
        return
      }
      const key = upstreamKey(req.headers.authorization)
      mockState.hitsByKey.set(key, (mockState.hitsByKey.get(key) ?? 0) + 1)
      const finish = delayedResponse(res, key)
      res.once('close', () => {
        if (!res.writableEnded) {
          mockState.abortedByKey.set(key, (mockState.abortedByKey.get(key) ?? 0) + 1)
          finish.cancel()
        }
      })
    })
  })
}

function delayedResponse(res: http.ServerResponse, key: string): { cancel: () => void } {
  if (key === 'timeout-always') {
    return { cancel: () => {} }
  }
  const hits = mockState.hitsByKey.get(key) ?? 0
  if (key === 'precheck-recover' && hits <= 3) {
    const timer = setTimeout(() => {
      if (!res.destroyed) {
        sendJsonError(res, 500, 'mock precheck staged failure')
      }
    }, 100)
    return {
      cancel: () => clearTimeout(timer)
    }
  }
  const delayMs = key === 'retry-once' && hits === 1
    ? 12_000
    : key === 'cancel-running' || key === 'stale-session'
      ? 30_000
      : 500
  const timer = setTimeout(() => {
    if (!res.destroyed) {
      sendResponsesCompleted(res, `OK ${key}`)
    }
  }, delayMs)
  return {
    cancel: () => clearTimeout(timer)
  }
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
      id: 'resp_account_test_mock_ai_edge',
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
