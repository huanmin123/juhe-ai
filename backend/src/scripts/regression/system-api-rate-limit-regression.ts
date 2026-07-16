import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-api-rate-limit-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-api-rate-limit-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  { createSystemApiApp },
  repositories,
  { clearSystemApiRateLimitStateForTest },
  clientIpStats,
  clientIpPolicyCache,
  readWorkerPool
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/system-api/system-api-app.js'),
  import('../../storage/repositories.js'),
  import('../../modules/system-api/system-api-rate-limit.middleware.js'),
  import('../../storage/client-ip-stats.repository.js'),
  import('../../modules/gateway/runtime/client-ip-policy-cache.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

async function main(): Promise<void> {
  let server: http.Server | undefined
  try {
    assertSystemApiRateLimitSourceOrder()
    assertDefaultSettings()
    prepareAdminSessionAccount()

    const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', trustProxy: true })
    server = app.listen(0, '127.0.0.1')
    await listen(server)
    const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`
    const adminCookie = createAdminCookie()

    await assertIpReadLimit(baseUrl)
    await assertRateLimitCannotBeDisabled()
    await assertAuthenticatedUserLimit(baseUrl, adminCookie)
    await assertAllowlistedIpBypassesRateLimit(baseUrl, adminCookie)
    await assertTestAppBypassesRateLimit(adminCookie)

    console.log('后台系统 API 限流回归通过：限流位于 body parser 前，阈值可配置且固定启用，IP 与登录用户超限返回 429，健康检查、IP 白名单和测试 app 实例旁路符合预期')
  } finally {
    await closeServer(server)
    try {
      await readWorkerPool.closeSqliteReadWorkerPool()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function assertSystemApiRateLimitSourceOrder(): void {
  const source = readFileSync(resolve('src/modules/system-api/system-api-app.ts'), 'utf8')
  const ipLimiter = source.indexOf('app.use(systemApiPrefix, systemApiIpRateLimit)')
  const jsonParser = source.indexOf('app.use(systemApiPrefix, express.json')
  const authMiddleware = source.indexOf('app.use(systemApiPrefix, requireAuth)')
  const userLimiter = source.indexOf('app.use(systemApiPrefix, systemApiAuthenticatedRateLimit)')
  assert(ipLimiter >= 0, '系统 API IP 级限流必须挂载在应用入口')
  assert(jsonParser >= 0, '系统 API JSON parser 必须存在')
  assert(ipLimiter < jsonParser, 'IP 级限流必须位于 JSON body parser 前')
  assert(authMiddleware >= 0, '系统 API 登录态中间件必须存在')
  assert(userLimiter > authMiddleware, '用户级限流必须位于 requireAuth 之后')
}

function assertDefaultSettings(): void {
  const settings = repositories.getSettings()
  assert.equal(Object.prototype.hasOwnProperty.call(settings, 'systemApiRateLimitEnabled'), false, '后台接口限流开关不应暴露为系统设置')
  assert.equal(settings.systemApiRateLimitIpReadPerMinute, 600, 'IP 读请求每分钟默认值应为 600')
  assert.equal(settings.systemApiRateLimitIpReadBurstPer10Seconds, 120, 'IP 读请求突发默认值应为 120')
  assert.equal(settings.systemApiRateLimitIpWritePerMinute, 180, 'IP 写请求每分钟默认值应为 180')
  assert.equal(settings.systemApiRateLimitIpWriteBurstPer10Seconds, 40, 'IP 写请求突发默认值应为 40')
  assert.equal(settings.systemApiRateLimitUserReadPerMinute, 300, '登录用户读请求每分钟默认值应为 300')
  assert.equal(settings.systemApiRateLimitUserWritePerMinute, 120, '登录用户写请求每分钟默认值应为 120')
}

function prepareAdminSessionAccount(): void {
  databaseModule.getBusinessDatabase()
    .prepare("UPDATE system_accounts SET must_change_password = 0, updated_at = ? WHERE id = 'sys_admin'")
    .run(new Date().toISOString())
}

function createAdminCookie(): string {
  const session = repositories.createSession('sys_admin')
  return `juhe_ai_session=${encodeURIComponent(session.token)}`
}

async function assertIpReadLimit(baseUrl: string): Promise<void> {
  repositories.updateSettings({
    systemApiRateLimitIpReadPerMinute: 2,
    systemApiRateLimitIpReadBurstPer10Seconds: 0,
    systemApiRateLimitIpWritePerMinute: 1000,
    systemApiRateLimitIpWriteBurstPer10Seconds: 1000,
    systemApiRateLimitUserReadPerMinute: 1000,
    systemApiRateLimitUserWritePerMinute: 1000
  })
  clearSystemApiRateLimitStateForTest()

  const clientIp = '198.51.100.101'
  await assertStatus(baseUrl, '/__aisys__/api/settings/public', 200, { clientIp })
  await assertStatus(baseUrl, '/__aisys__/api/settings/public', 200, { clientIp })
  const blocked = await request(baseUrl, '/__aisys__/api/settings/public', { clientIp })
  assert.equal(blocked.status, 429, `IP 读请求超限应返回 429，实际 HTTP ${blocked.status}: ${await blocked.text()}`)
  assert(blocked.headers.get('retry-after'), 'IP 读请求超限应返回 Retry-After')

  await assertStatus(baseUrl, '/__aisys__/api/health', 200, { clientIp })
}

async function assertRateLimitCannotBeDisabled(): Promise<void> {
  assert.throws(
    () => repositories.updateSettings({ systemApiRateLimitEnabled: false }),
    /未知系统设置字段：systemApiRateLimitEnabled/,
    '后台接口限流属于固定启用能力，不能通过系统设置关闭'
  )
}

async function assertAuthenticatedUserLimit(baseUrl: string, adminCookie: string): Promise<void> {
  repositories.updateSettings({
    systemApiRateLimitIpReadPerMinute: 1000,
    systemApiRateLimitIpReadBurstPer10Seconds: 1000,
    systemApiRateLimitIpWritePerMinute: 1000,
    systemApiRateLimitIpWriteBurstPer10Seconds: 1000,
    systemApiRateLimitUserReadPerMinute: 1,
    systemApiRateLimitUserWritePerMinute: 1000
  })
  clearSystemApiRateLimitStateForTest()

  const clientIp = '198.51.100.103'
  await assertStatus(baseUrl, '/__aisys__/api/settings', 200, { clientIp, cookie: adminCookie })
  const blocked = await request(baseUrl, '/__aisys__/api/settings', { clientIp, cookie: adminCookie })
  assert.equal(blocked.status, 429, `登录用户读请求超限应返回 429，实际 HTTP ${blocked.status}: ${await blocked.text()}`)
  assert(blocked.headers.get('retry-after'), '登录用户读请求超限应返回 Retry-After')

  await assertStatus(baseUrl, '/__aisys__/api/settings/public', 200, { clientIp })
}

async function assertAllowlistedIpBypassesRateLimit(baseUrl: string, adminCookie: string): Promise<void> {
  repositories.updateSettings({
    systemApiRateLimitIpReadPerMinute: 1,
    systemApiRateLimitIpReadBurstPer10Seconds: 1,
    systemApiRateLimitIpWritePerMinute: 1,
    systemApiRateLimitIpWriteBurstPer10Seconds: 1,
    systemApiRateLimitUserReadPerMinute: 1,
    systemApiRateLimitUserWritePerMinute: 1
  })
  clearSystemApiRateLimitStateForTest()

  const clientIp = '198.51.100.104'
  seedClientIpRegistry(clientIp)
  const normalized = clientIpStats.normalizeClientIpForStats(clientIp)
  assert(normalized, '白名单回归 IP 应可规范化')
  clientIpStats.createClientIpPolicy({
    ipHash: normalized.ipHash,
    policyType: 'allowlist',
    reason: 'system api rate limit regression',
    actorSystemAccountId: 'sys_admin'
  })
  await clientIpPolicyCache.reloadClientIpPolicyCacheLocal()

  await assertStatus(baseUrl, '/__aisys__/api/settings/public', 200, { clientIp })
  await assertStatus(baseUrl, '/__aisys__/api/settings/public', 200, { clientIp })
  await assertStatus(baseUrl, '/__aisys__/api/settings', 200, { clientIp, cookie: adminCookie })
  await assertStatus(baseUrl, '/__aisys__/api/settings', 200, { clientIp, cookie: adminCookie })
}

async function assertTestAppBypassesRateLimit(adminCookie: string): Promise<void> {
  repositories.updateSettings({
    systemApiRateLimitIpReadPerMinute: 1,
    systemApiRateLimitIpReadBurstPer10Seconds: 1,
    systemApiRateLimitIpWritePerMinute: 1,
    systemApiRateLimitIpWriteBurstPer10Seconds: 1,
    systemApiRateLimitUserReadPerMinute: 1,
    systemApiRateLimitUserWritePerMinute: 1
  })
  clearSystemApiRateLimitStateForTest()

  const app = createSystemApiApp({
    systemApiPrefix: '/__aisys__/api',
    trustProxy: true,
    bypassSystemApiRateLimitForTest: true
  })
  const server = app.listen(0, '127.0.0.1')
  try {
    await listen(server)
    const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`
    const clientIp = '198.51.100.105'
    await assertStatus(baseUrl, '/__aisys__/api/settings/public', 200, { clientIp })
    await assertStatus(baseUrl, '/__aisys__/api/settings/public', 200, { clientIp })
    await assertStatus(baseUrl, '/__aisys__/api/settings', 200, { clientIp, cookie: adminCookie })
    await assertStatus(baseUrl, '/__aisys__/api/settings', 200, { clientIp, cookie: adminCookie })
  } finally {
    await closeServer(server)
  }
}

function seedClientIpRegistry(clientIp: string): void {
  const normalized = clientIpStats.normalizeClientIpForStats(clientIp)
  assert(normalized, '白名单回归 IP 应可规范化')
  const now = new Date().toISOString()
  databaseModule.getStatsDatabase().prepare(`
    INSERT INTO client_ip_registry (
      ip_hash, bucket_no, aggregate_ip_key, client_ip, ip_version,
      first_seen_at, last_seen_at, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(ip_hash) DO UPDATE SET
      last_seen_at = excluded.last_seen_at,
      updated_at = excluded.updated_at
  `).run(
    normalized.ipHash,
    normalized.bucketNo,
    normalized.aggregateIpKey,
    normalized.clientIp,
    normalized.ipVersion,
    now,
    now,
    now,
    now
  )
}

async function assertStatus(
  baseUrl: string,
  path: string,
  status: number,
  options: RequestOptions = {}
): Promise<void> {
  const response = await request(baseUrl, path, options)
  const text = await response.text()
  assert.equal(response.status, status, `${path} 应返回 HTTP ${status}，实际 HTTP ${response.status}: ${text}`)
}

interface RequestOptions {
  clientIp?: string
  cookie?: string
}

function request(baseUrl: string, path: string, options: RequestOptions = {}): Promise<Response> {
  const headers: Record<string, string> = {}
  if (options.clientIp) headers['x-forwarded-for'] = options.clientIp
  if (options.cookie) headers.cookie = options.cookie
  return fetch(`${baseUrl}${path}`, { headers })
}

function listen(server: http.Server): Promise<void> {
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

main().catch((error) => {
  console.error('\n后台系统 API 限流回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
