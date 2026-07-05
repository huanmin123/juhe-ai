import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { performance } from 'node:perf_hooks'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-system-api-read-burst-'))

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
process.env.JUHE_AI_SECRET = 'system-api-read-burst-secret'
process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'
process.env.JUHE_AI_LOG_FILE_ENABLED = 'false'
process.env.JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE = '4'

let server: http.Server | undefined
let closeSqliteReadWorkerPool: (() => Promise<void>) | undefined
let closeStorageDatabases: (() => void) | undefined

try {
  const [
    { createSystemApiApp },
    { captchaAnswerForTest },
    { GPT_OPENAI_V1_PROFILE_ID },
    { logger },
    repositories,
    databaseModule,
    readWorkerPool,
    { createApiKeyRecordWithRouteStrategy }
  ] = await Promise.all([
    import('../../modules/system-api/system-api-app.js'),
    import('../../modules/auth/captcha.service.js'),
    import('../../domain/provider-protocol.js'),
    import('../../shared/logger.js'),
    import('../../storage/repositories.js'),
    import('../../storage/database.js'),
    import('../../storage/sqlite-read-worker-pool.js'),
    import('../shared/route-strategy-fixture.js')
  ])
  logger.level = 'silent'
  closeStorageDatabases = databaseModule.closeStorageDatabases
  closeSqliteReadWorkerPool = readWorkerPool.closeSqliteReadWorkerPool

  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  repositories.updateSettings({
    systemApiRateLimitIpReadPerMinute: 1_000_000,
    systemApiRateLimitIpReadBurstPer10Seconds: 1_000_000,
    systemApiRateLimitIpWritePerMinute: 1_000_000,
    systemApiRateLimitIpWriteBurstPer10Seconds: 1_000_000,
    systemApiRateLimitUserReadPerMinute: 1_000_000,
    systemApiRateLimitUserWritePerMinute: 1_000_000
  })
  const group = repositories.createGroup({
    name: '读突发回归分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '读突发回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-system-api-read-burst',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    status: 'active'
  }, access)
  repositories.updateAccountTags(account.id, ['读突发回归标签'], access)
  const routeStrategy = repositories.createRouteStrategy({
    name: '读突发回归路由',
    mode: 'normal',
    groupBindings: [{
      groupId: group.id,
      priority: 1,
      weight: 100,
      status: 'active'
    }]
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '读突发回归 API Key',
    routeStrategyId: routeStrategy.id,
    status: 'active'
  }, access)
  const proxy = repositories.createProxy({
    name: '读突发回归代理',
    type: 'http',
    host: '127.0.0.1',
    port: 7890,
    enabled: true
  }, access)

  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', trustProxy: true })
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`
  const cookie = await login(baseUrl, captchaAnswerForTest)
  const expectations: BurstExpectations = {
    accountId: account.id,
    accountName: account.name,
    groupId: group.id,
    routeStrategyId: routeStrategy.id,
    apiKeyId: apiKey.id,
    proxyId: proxy.id,
    tagName: '读突发回归标签'
  }

  const targets = [
    '/__aisys__/api/auth/me',
    '/__aisys__/api/my-accounts?page=1&pageSize=20&sorts=priority:asc',
    '/__aisys__/api/accounts?page=1&pageSize=20&sorts=priority:asc',
    `/__aisys__/api/accounts/${account.id}`,
    `/__aisys__/api/accounts/${account.id}/advanced`,
    '/__aisys__/api/accounts/options?limit=50',
    '/__aisys__/api/accounts/test-tasks?ids=missing_task',
    '/__aisys__/api/accounts/tags',
    '/__aisys__/api/providers/options',
    '/__aisys__/api/providers/models/options?protocol=openai',
    '/__aisys__/api/proxies/options?limit=50',
    '/__aisys__/api/groups?page=1&pageSize=20',
    '/__aisys__/api/groups/options?limit=50',
    '/__aisys__/api/api-keys?page=1&pageSize=20',
    '/__aisys__/api/route-strategies/options?limit=50',
    '/__aisys__/api/system-accounts/options?limit=50',
    '/__aisys__/api/settings',
    '/__aisys__/api/stats/usage-window',
    '/__aisys__/api/stats/ai-performance/accounts?limit=20',
    '/__aisys__/api/response-inspection-policies'
  ]

  for (const target of targets) {
    await getOk(baseUrl, target, cookie, expectations)
  }

  const readWorkerRuntimeBeforeBurst = readWorkerPool.getSqliteReadWorkerPoolRuntime()
  assert(readWorkerRuntimeBeforeBurst.enabled, 'System API 读突发回归必须在 db-service 角色启用 SQLite read worker')
  const summaries: Array<{ path: string; count: number; p50: number; p95: number; max: number }> = []
  for (const target of targets) {
    const durations = await burstGet(baseUrl, target, cookie, expectations, {
      total: 24,
      concurrency: 6
    })
    const summary = summarize(target, durations)
    summaries.push(summary)
    assert(summary.p95 < 750, `${target} 连续读 p95 过高：${summary.p95.toFixed(1)}ms`)
    assert(summary.max < 1500, `${target} 连续读 max 过高：${summary.max.toFixed(1)}ms`)
  }
  const readWorkerRuntimeAfterBurst = readWorkerPool.getSqliteReadWorkerPoolRuntime()
  assert(
    readWorkerRuntimeAfterBurst.handledJobs > readWorkerRuntimeBeforeBurst.handledJobs,
    `System API 读突发必须命中 SQLite read worker，before=${readWorkerRuntimeBeforeBurst.handledJobs} after=${readWorkerRuntimeAfterBurst.handledJobs}`
  )
  assert.equal(
    readWorkerRuntimeAfterBurst.timedOutJobs,
    readWorkerRuntimeBeforeBurst.timedOutJobs,
    'System API 读突发不应导致 SQLite read worker 超时'
  )
  assert.equal(
    readWorkerRuntimeAfterBurst.restartedWorkers,
    readWorkerRuntimeBeforeBurst.restartedWorkers,
    'System API 读突发不应重启 SQLite read worker'
  )

  console.log(`System API 读突发回归通过：${summaries.map((item) => `${item.path} p50=${item.p50.toFixed(1)}ms p95=${item.p95.toFixed(1)}ms max=${item.max.toFixed(1)}ms`).join(' | ')}`)
} finally {
  if (server) {
    await closeServer(server)
  }
  await closeSqliteReadWorkerPool?.().catch(() => undefined)
  closeStorageDatabases?.()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function login(baseUrl: string, captchaAnswerForTest: (captchaId: string) => string | undefined): Promise<string> {
  const captcha = await getEnvelope<{ captchaId: string }>(baseUrl, '/__aisys__/api/auth/captcha')
  const captchaCode = captchaAnswerForTest(captcha.captchaId)
  assert.ok(captchaCode, '读突发回归应能读取验证码答案')
  const response = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    signal: AbortSignal.timeout(10_000),
    body: JSON.stringify({
      username: 'admin',
      password: 'admin',
      captchaId: captcha.captchaId,
      captchaCode
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `读突发回归登录应成功，实际 HTTP ${response.status}: ${text}`)
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert.ok(cookie, '读突发回归登录应返回 session cookie')
  return cookie
}

async function burstGet(
  baseUrl: string,
  path: string,
  cookie: string,
  expectations: BurstExpectations,
  options: { total: number; concurrency: number }
): Promise<number[]> {
  const durations: number[] = []
  let nextIndex = 0
  const workers = Array.from({ length: options.concurrency }, async () => {
    while (nextIndex < options.total) {
      nextIndex += 1
      const startedAt = performance.now()
      await getOk(baseUrl, path, cookie, expectations)
      durations.push(performance.now() - startedAt)
    }
  })
  await Promise.all(workers)
  return durations
}

interface BurstExpectations {
  accountId: string
  accountName: string
  groupId: string
  routeStrategyId: string
  apiKeyId: string
  proxyId: string
  tagName: string
}

async function getOk(baseUrl: string, path: string, cookie: string, expectations: BurstExpectations): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, {
    headers: { cookie },
    signal: AbortSignal.timeout(10_000)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${path} 应返回 HTTP 200，实际 HTTP ${response.status}: ${text}`)
  const data = (JSON.parse(text) as { data?: unknown }).data
  assertTargetPayload(path, data, expectations)
}

function assertTargetPayload(path: string, data: unknown, expectations: BurstExpectations): void {
  if (path.includes('/accounts/test-tasks')) {
    assert(Array.isArray(data), `${path} 应返回任务数组，不能返回空 envelope`)
    return
  }
  if (path.includes('/accounts/tags')) {
    assert(arrayIncludesObject(data, 'name', expectations.tagName), `${path} 应返回 seed 标签`)
    return
  }
  if (path.includes('/accounts/options')) {
    assert(arrayIncludesObject(data, 'id', expectations.accountId), `${path} 应返回 seed 账号选项`)
    return
  }
  if (path.includes(`/accounts/${expectations.accountId}`)) {
    assert.equal((data as { id?: string } | undefined)?.id, expectations.accountId, `${path} 应返回 seed 账号详情`)
    return
  }
  if (path.includes('/my-accounts') || path.includes('/accounts?page=')) {
    assert(pageIncludesObject(data, 'id', expectations.accountId), `${path} 应返回 seed 账号列表`)
    return
  }
  if (path.includes('/proxies/options')) {
    assert(arrayIncludesObject(data, 'id', expectations.proxyId), `${path} 应返回 seed 代理选项`)
    return
  }
  if (path.includes('/groups/options')) {
    assert(arrayIncludesObject(data, 'id', expectations.groupId), `${path} 应返回 seed 分组选项`)
    return
  }
  if (path.includes('/groups?page=')) {
    assert(pageIncludesObject(data, 'id', expectations.groupId), `${path} 应返回 seed 分组列表`)
    return
  }
  if (path.includes('/api-keys?page=')) {
    assert(pageIncludesObject(data, 'id', expectations.apiKeyId), `${path} 应返回 seed API Key 列表`)
    return
  }
  if (path.includes('/route-strategies/options')) {
    assert(arrayIncludesObject(data, 'id', expectations.routeStrategyId), `${path} 应返回 seed 路由策略选项`)
    return
  }
  if (path.includes('/auth/me')) {
    assert.equal((data as { id?: string } | undefined)?.id, 'sys_admin', `${path} 应返回登录账户信息`)
    return
  }
  assert.notEqual(data, undefined, `${path} 应返回 data envelope`)
}

function pageIncludesObject(data: unknown, key: string, value: string): boolean {
  const items = (data as { items?: unknown[] } | undefined)?.items
  return Array.isArray(items) && items.some((item) => (item as Record<string, unknown>)[key] === value)
}

function arrayIncludesObject(data: unknown, key: string, value: string): boolean {
  return Array.isArray(data) && data.some((item) => (item as Record<string, unknown>)[key] === value)
}

async function getEnvelope<T>(baseUrl: string, path: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    signal: AbortSignal.timeout(10_000)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `${path} 应返回 HTTP 200，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as { data: T }).data
}

function summarize(path: string, durations: number[]): { path: string; count: number; p50: number; p95: number; max: number } {
  const sorted = [...durations].sort((left, right) => left - right)
  return {
    path,
    count: sorted.length,
    p50: percentile(sorted, 0.5),
    p95: percentile(sorted, 0.95),
    max: sorted[sorted.length - 1] ?? 0
  }
}

function percentile(sorted: number[], value: number): number {
  if (sorted.length === 0) return 0
  const index = Math.min(sorted.length - 1, Math.max(0, Math.ceil(sorted.length * value) - 1))
  return sorted[index] ?? 0
}

async function listen(server: http.Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(address && typeof address === 'object', '测试服务监听地址无效')
  return { port: address.port }
}

async function closeServer(server: http.Server): Promise<void> {
  if (!server.listening) return
  await new Promise<void>((resolve, reject) => {
    server.close((error) => {
      if (error) reject(error)
      else resolve()
    })
  })
}
