import { strict as assert } from 'node:assert'
import { mkdtempSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

const inputURL = requiredEnv('JUHE_AI_OPERATION_LOG_INPUT_URL')
requiredEnv('JUHE_AI_OPERATION_LOG_INPUT_SECRET')
const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-f4-worker-producer-'))

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
process.env.JUHE_AI_SECRET = 'f4-worker-producer-smoke-secret'
process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'
process.env.JUHE_AI_LOG_FILE_ENABLED = 'false'
process.env.JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE = '4'
process.env.NODE_ENV = 'test'

let server: http.Server | undefined
let closeStorageDatabases: (() => void) | undefined
let closeSqliteReadWorkerPool: (() => Promise<void>) | undefined

try {
  const [
    { createSystemApiApp },
    { captchaAnswerForTest },
    { runtimeConfig },
    { logger },
    databaseModule,
    readWorkerPool
  ] = await Promise.all([
    import('../../modules/system-api/system-api-app.js'),
    import('../../modules/auth/captcha.service.js'),
    import('../../config/runtime.js'),
    import('../../shared/logger.js'),
    import('../../storage/database.js'),
    import('../../storage/sqlite-read-worker-pool.js')
  ])
  logger.level = 'silent'
  closeStorageDatabases = databaseModule.closeStorageDatabases
  closeSqliteReadWorkerPool = readWorkerPool.closeSqliteReadWorkerPool
  databaseModule.getBusinessDatabase()
  databaseModule.getDatasetDatabase()
  databaseModule.getUsageCatalogDatabase()
  const statsDatabase = databaseModule.getStatsDatabase()

  const app = createSystemApiApp({
    systemApiPrefix: '/__aisys__/api',
    trustProxy: true,
    bypassSystemApiRateLimitForTest: true
  })
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const baseURL = `http://127.0.0.1:${addressPort(server)}`
  const cookie = await login(baseURL, captchaAnswerForTest)

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'stats-worker'
  const ipHash = 'a'.repeat(64)
  const now = new Date().toISOString()
  statsDatabase.prepare(`
    INSERT INTO client_ip_registry (
      ip_hash, bucket_no, aggregate_ip_key, client_ip, ip_version, first_seen_at, last_seen_at, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(ipHash, 0, '127.0.0.1', '127.0.0.1', 4, now, now, now, now)
  const blacklist = await request(baseURL, `/__aisys__/api/ip-stats/${ipHash}/blacklist`, cookie, {
    method: 'POST',
    body: { reason: 'F4 worker producer smoke', durationMinutes: 1 }
  })
  assert.equal(blacklist.status, 200, `ip-stats 真实 stats writer 写入应成功：${blacklist.text}`)
  const ipLog = await findOperation(baseURL, cookie, 'client_ip_stats', 'blacklist')
  await assertDetail(baseURL, cookie, ipLog.id, 'client_ip_stats.blacklist', `/__aisys__/api/ip-stats/${ipHash}/blacklist`)

  runtimeConfig.workerRole = 'ingest-worker'
  const cleanup = await request(baseURL, '/__aisys__/api/table-monitor/non-business-data/cleanup', cookie, {
    method: 'POST',
    body: { cutoffAt: new Date(Date.now() - 60_000).toISOString() }
  })
  assert.equal(cleanup.status, 200, `table-monitor 真实 maintenance 投递应成功：${cleanup.text}`)
  assert.equal(envelope<{ queued?: boolean }>(cleanup.text).queued, true, `table-monitor 必须成功投递 maintenance job：${cleanup.text}`)
  const tableLog = await findOperation(baseURL, cookie, 'table_monitor', 'cleanup_non_business_data')
  await assertDetail(baseURL, cookie, tableLog.id, 'table_monitor.cleanup_non_business_data', '/__aisys__/api/table-monitor/non-business-data/cleanup')

  console.log(`F4 worker producer smoke passed: ip-stats, table-monitor (${inputURL})`)
} finally {
  if (server) await close(server)
  await closeSqliteReadWorkerPool?.().catch(() => undefined)
  closeStorageDatabases?.()
  await removeTempRoot()
}

async function login(baseURL: string, captchaAnswerForTest: (captchaID: string) => string | undefined): Promise<string> {
  const captcha = await request(baseURL, '/__aisys__/api/auth/captcha')
  assert.equal(captcha.status, 200, `worker producer smoke captcha 读取应成功：${captcha.text}`)
  const captchaID = envelope<{ captchaId: string }>(captcha.text).captchaId
  const captchaCode = captchaAnswerForTest(captchaID)
  assert(captchaCode, 'worker producer smoke 必须能取得 captcha 答案')
  const response = await request(baseURL, '/__aisys__/api/auth/login', undefined, {
    method: 'POST',
    body: { username: 'admin', password: 'admin', captchaId: captchaID, captchaCode }
  })
  assert.equal(response.status, 200, `worker producer smoke 管理员登录应成功：${response.text}`)
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert(cookie, 'worker producer smoke 登录必须返回会话 cookie')
  return cookie
}

async function findOperation(baseURL: string, cookie: string, module: string, action: string): Promise<{ id: string }> {
  return eventually(async () => {
    const response = await request(baseURL, `/__aisys__/api/operation-logs?module=${encodeURIComponent(module)}&action=${encodeURIComponent(action)}&page=1&pageSize=20`, cookie)
    assert.equal(response.status, 200, `Go owner 管理读取 ${module}.${action} 应成功：${response.text}`)
    const data = envelope<{ items?: Array<{ id: string; module: string; action: string }> }>(response.text)
    return data.items?.find((item) => item.module === module && item.action === action)
  }, 10_000, `真实 ${module}.${action} 未通过 Node -> Go F4 -> Node 管理读回`)
}

async function assertDetail(baseURL: string, cookie: string, id: string, operationKey: string, path: string): Promise<void> {
  const response = await request(baseURL, `/__aisys__/api/operation-logs/${encodeURIComponent(id)}`, cookie)
  assert.equal(response.status, 200, `Go owner 管理详情应成功：${response.text}`)
  const detail = envelope<{ operationKey?: string; path?: string }>(response.text)
  assert.equal(detail.operationKey, operationKey)
  assert.equal(detail.path, path)
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

function envelope<T>(text: string): T {
  return (JSON.parse(text) as { data: T }).data
}

async function eventually<T>(operation: () => Promise<T | undefined>, timeoutMs: number, message: string): Promise<T> {
  const deadline = Date.now() + timeoutMs
  let lastError: unknown
  while (Date.now() < deadline) {
    try {
      const result = await operation()
      if (result !== undefined) return result
    } catch (error) {
      lastError = error
    }
    await delay(50)
  }
  throw new Error(lastError ? `${message}: ${lastError instanceof Error ? lastError.message : String(lastError)}` : message)
}

async function listen(serverInstance: http.Server): Promise<void> {
  if (serverInstance.listening) return
  await new Promise<void>((resolveListen, reject) => {
    serverInstance.once('listening', resolveListen)
    serverInstance.once('error', reject)
  })
}

function addressPort(serverInstance: http.Server): number {
  const address = serverInstance.address()
  assert(address && typeof address === 'object', 'worker producer smoke 监听地址无效')
  return address.port
}

async function close(serverInstance: http.Server): Promise<void> {
  if (!serverInstance.listening) return
  await new Promise<void>((resolveClose, reject) => serverInstance.close((error) => error ? reject(error) : resolveClose()))
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

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} is required for the F4 worker producer smoke`)
  return value
}
