import { strict as assert } from 'node:assert'
import { spawn, type ChildProcess } from 'node:child_process'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import type { AccountSummary, AccountTestResult } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'

const currentDir = dirname(fileURLToPath(import.meta.url))
const backendRoot = resolve(currentDir, '../../..')
const projectRoot = resolve(backendRoot, '..')
const useShellSpawn = process.platform === 'win32'
const tempRoot = resolve(tmpdir(), `juhe-ai-account-batch-test-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const batchSize = 12
const batchConcurrency = batchSize
const accountTestTaskMaxWaitMs = 60_000
const pollIntervalMs = 100

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'account-batch-test-mock-ai-secret'
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

interface BatchTaskSummary {
  accountName: string
  taskId: string
  success: boolean
  durationMs: number
  statuses: string[]
}

const mockState = {
  hitsByKey: new Map<string, number>(),
  inFlight: 0,
  maxInFlight: 0
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
  repositories.updateSettings({ systemApiRateLimitEnabled: false })
  mockUpstream = createMockAIUpstream()
  mockUpstream.listen(0, '127.0.0.1')
  await onceListening(mockUpstream)
  const mockBaseUrl = `http://127.0.0.1:${serverPort(mockUpstream)}`

  const backendPort = await freePort()
  const backendBaseUrl = `http://127.0.0.1:${backendPort}`
  const admin = repositories.createSystemAccount({
    username: `batch_mock_admin_${Date.now()}`.replace(/[^a-zA-Z0-9_]/g, '_'),
    displayName: '批量测试MockAI管理员',
    password: 'password',
    role: 'admin',
    status: 'active',
    mustChangePassword: false
  })
  const session = repositories.createSession(admin.id, 1)
  const cookie = `juhe_ai_session=${session.token}`
  repositories.updateSettings({ systemApiRateLimitEnabled: false })
  prepareMainDatabaseSchemasForChildServer()

  backendProcess = startBackendServer(backendPort)
  await waitForHealth(backendBaseUrl, backendProcess)
  await waitForApiReady(backendBaseUrl, cookie, backendProcess)
  const me = await getEnvelope<{ username: string; role: string }>(backendBaseUrl, '/auth/me', cookie)
  assert.equal(me.username, admin.username, '临时后端应识别脚本写入的管理员会话')

  const group = await postEnvelope<{ id: string; name: string }>(backendBaseUrl, '/groups', cookie, {
    name: `批量测试 mock AI 分组 ${Date.now()}`,
    providerCode: 'gpt'
  })
  const accounts: AccountSummary[] = []
  for (let index = 0; index < batchSize; index += 1) {
    const account = await postEnvelope<AccountSummary>(backendBaseUrl, '/accounts', cookie, {
      providerCode: 'gpt',
      name: `批量测试 mock AI 账户 ${index + 1}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-batch-${index + 1}`,
        base_url: mockBaseUrl
      },
      groupId: group.id
    })
    assert.equal(account.status, 'pending_test', '未携带激活测试任务创建的账号应先进入待测试状态')
    accounts.push(account)
  }

  const startedAt = Date.now()
  const summaries = await runWithConcurrency(accounts, batchConcurrency, async (account) => runBatchAccountTestItem(backendBaseUrl, cookie, account))
  const totalDurationMs = Date.now() - startedAt

  assert.equal(summaries.length, batchSize, '批量测试应返回每个账号的结果')
  assert.equal(summaries.filter((item) => item.success).length, batchSize, 'mock AI 批量测试应全部通过')
  for (let index = 0; index < batchSize; index += 1) {
    assert.equal(mockState.hitsByKey.get(`batch-${index + 1}`), 1, `账号 ${index + 1} 应真实命中 mock 上游一次`)
  }
  assert.equal(mockState.maxInFlight, batchSize, `默认账号测试后台并发 100 不应限制 ${batchSize} 个直接提交任务，实际 mock 上游最大并发 ${mockState.maxInFlight}`)

  console.log(JSON.stringify({
    message: '批量账号测试 mock AI 真实链路通过',
    backendBaseUrl,
    mockBaseUrl,
    totalAccounts: batchSize,
    successCount: summaries.filter((item) => item.success).length,
    totalDurationMs,
    maxMockUpstreamInFlight: mockState.maxInFlight,
    tasks: summaries
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

async function runBatchAccountTestItem(baseUrl: string, cookie: string, account: AccountSummary): Promise<BatchTaskSummary> {
  const submittedAt = Date.now()
  const task = await postEnvelope<AccountTestTask>(baseUrl, `/accounts/${account.id}/test`, cookie, { model: 'gpt-5.5' })
  const statuses = [`0ms:${task.status}:${task.message ?? ''}`]
  let latest = task
  while (Date.now() - submittedAt <= accountTestTaskMaxWaitMs) {
    if (latest.status === 'success' || latest.status === 'failed') {
      assert(latest.result, `任务 ${latest.id} 已结束但没有结果`)
      return {
        accountName: account.name,
        taskId: latest.id,
        success: latest.result.success,
        durationMs: Date.now() - submittedAt,
        statuses
      }
    }
    if (latest.status === 'canceled') {
      throw new Error(`任务 ${latest.id} 被取消：${latest.message ?? ''}`)
    }
    await sleep(pollIntervalMs)
    const tasks = await getEnvelope<AccountTestTask[]>(baseUrl, `/accounts/test-tasks?ids=${encodeURIComponent(task.id)}`, cookie)
    const next = tasks.find((item) => item.id === task.id)
    assert(next, `任务 ${task.id} 应可查询`)
    if (next.status !== latest.status || next.message !== latest.message) {
      statuses.push(`${Date.now() - submittedAt}ms:${next.status}:${next.message ?? ''}`)
    }
    latest = next
  }
  throw new Error(`任务 ${task.id} 超过 ${accountTestTaskMaxWaitMs}ms 仍未结束，最后状态：${latest.status} ${latest.message ?? ''}`)
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
    process.stdout.write(`[batch-test-backend] ${String(chunk)}`)
  })
  child.stderr?.on('data', (chunk) => {
    process.stderr.write(`[batch-test-backend] ${String(chunk)}`)
  })
  return child
}

function prepareMainDatabaseSchemasForChildServer(): void {
  databaseModule.getDatasetDatabase()
  databaseModule.getStatsDatabase()
  databaseModule.closeStorageDatabases()
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

async function runWithConcurrency<TItem, TResult>(
  items: TItem[],
  concurrency: number,
  task: (item: TItem, index: number) => Promise<TResult>
): Promise<TResult[]> {
  const results: TResult[] = new Array(items.length)
  let nextIndex = 0
  const workerCount = Math.min(Math.max(1, concurrency), items.length)
  async function runWorker(): Promise<void> {
    while (nextIndex < items.length) {
      const index = nextIndex
      nextIndex += 1
      results[index] = await task(items[index], index)
    }
  }
  await Promise.all(Array.from({ length: workerCount }, runWorker))
  return results
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
      mockState.inFlight += 1
      mockState.maxInFlight = Math.max(mockState.maxInFlight, mockState.inFlight)
      setTimeout(() => {
        mockState.inFlight -= 1
        if (!res.destroyed) {
          sendResponsesCompleted(res, `OK ${key}`)
        }
      }, 1000)
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
      id: 'resp_account_batch_test_mock_ai',
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
