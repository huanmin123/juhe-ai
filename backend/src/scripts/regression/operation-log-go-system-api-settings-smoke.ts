import { strict as assert } from 'node:assert'
import { mkdtempSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

const inputURL = requiredEnv('JUHE_AI_OPERATION_LOG_INPUT_URL')
requiredEnv('JUHE_AI_OPERATION_LOG_INPUT_SECRET')
const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-f4-system-api-settings-'))

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
process.env.JUHE_AI_SECRET = 'f4-system-api-settings-smoke-secret'
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
    { logger },
    databaseModule,
    readWorkerPool
  ] = await Promise.all([
    import('../../modules/system-api/system-api-app.js'),
    import('../../modules/auth/captcha.service.js'),
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

  const patch = await request(baseURL, '/__aisys__/api/settings/global', cookie, {
    method: 'PATCH',
    body: { appName: 'F4 System API settings smoke' }
  })
  assert.equal(patch.status, 200, `真实 settings/global 业务写入应成功：${patch.text}`)

  const announcement = await request(baseURL, '/__aisys__/api/announcements', cookie, {
    method: 'POST',
    body: {
      title: 'F4 System API announcement smoke',
      content: 'F4 System API announcement smoke content',
      level: 'info',
      status: 'draft'
    }
  })
  assert.equal(announcement.status, 201, `真实 announcements 业务写入应成功：${announcement.text}`)

  const responsePolicy = await request(baseURL, '/__aisys__/api/response-inspection-policies', cookie, {
    method: 'POST',
    body: {
      name: 'F4 System API response policy smoke',
      enabled: true,
      priority: 10,
      scopeType: 'protocol',
      protocolCode: 'openai',
      match: { outputTextIncludes: ['F4-smoke'] },
      action: 'observe',
      notes: 'F4 System API response policy smoke'
    }
  })
  assert.equal(responsePolicy.status, 201, `真实 response-inspection-policies 业务写入应成功：${responsePolicy.text}`)

  const settingsItem = await assertOperation(baseURL, cookie, 'settings', 'update_global', '更新全局品牌设置')
  const settingsDetail = await readAdminDetail(baseURL, cookie, settingsItem.id, 'settings.update_global', 'global_settings')
  assert.equal(settingsDetail.method, 'PATCH')
  assert.equal(settingsDetail.path, '/__aisys__/api/settings/global')
  assert(settingsDetail.changes?.some((change) => change.field === 'appName' && change.after === 'F4 System API settings smoke'), '管理详情必须保留真实业务字段变更')
  await assertPersonalSummary(baseURL, cookie, settingsItem.id, true)

  const announcementItem = await assertOperation(baseURL, cookie, 'announcements', 'create', '创建公告：F4 System API announcement smoke')
  const announcementDetail = await readAdminDetail(baseURL, cookie, announcementItem.id, 'announcements.create', 'announcement')
  assert.equal(announcementDetail.path, '/__aisys__/api/announcements/')
  await assertPersonalSummary(baseURL, cookie, announcementItem.id, false)

  const policyItem = await assertOperation(baseURL, cookie, 'response_inspection_policies', 'create', '创建响应检查策略：F4 System API response policy smoke')
  const policyDetail = await readAdminDetail(baseURL, cookie, policyItem.id, 'response_inspection_policies.create', 'response_inspection_policy')
  assert.equal(policyDetail.path, '/__aisys__/api/response-inspection-policies/')
  await assertPersonalSummary(baseURL, cookie, policyItem.id, false)

  console.log(`F4 System API producer smoke passed: settings, announcements, response_inspection_policies (${inputURL})`)
} finally {
  if (server) await close(server)
  await closeSqliteReadWorkerPool?.().catch(() => undefined)
  closeStorageDatabases?.()
  await removeTempRoot()
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} is required for the F4 System API settings smoke`)
  return value
}

async function login(baseURL: string, captchaAnswerForTest: (captchaID: string) => string | undefined): Promise<string> {
  const captcha = await request(baseURL, '/__aisys__/api/auth/captcha')
  assert.equal(captcha.status, 200, `captcha 应成功：${captcha.text}`)
  const captchaID = envelope<{ captchaId: string }>(captcha.text).captchaId
  const captchaCode = captchaAnswerForTest(captchaID)
  assert.ok(captchaCode, '测试必须能取得 captcha 答案')
  const response = await request(baseURL, '/__aisys__/api/auth/login', undefined, {
    method: 'POST',
    body: { username: 'admin', password: 'admin', captchaId: captchaID, captchaCode }
  })
  assert.equal(response.status, 200, `登录应成功：${response.text}`)
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert.ok(cookie, '登录应返回 session cookie')
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

type OperationListItem = { id: string; module: string; action: string; summary: string }
type OperationDetail = { operationKey?: string; resourceType?: string; changes?: Array<{ field?: string; after?: unknown }>; method?: string; path?: string }

async function assertOperation(baseURL: string, cookie: string, module: string, action: string, summary: string): Promise<OperationListItem> {
  return eventually(async () => {
    const response = await request(baseURL, `/__aisys__/api/operation-logs?module=${encodeURIComponent(module)}&action=${encodeURIComponent(action)}&page=1&pageSize=20`, cookie)
    assert.equal(response.status, 200, `Go owner 管理读取 ${module}.${action} 应成功：${response.text}`)
    const data = envelope<{ items?: OperationListItem[] }>(response.text)
    return data.items?.find((candidate) => candidate.module === module && candidate.action === action && candidate.summary === summary)
  }, 10_000, `真实 ${module}.${action} 操作未通过 Node -> Go F4 -> Node 管理列表读回`)
}

async function readAdminDetail(baseURL: string, cookie: string, id: string, operationKey: string, resourceType: string): Promise<OperationDetail> {
  const response = await request(baseURL, `/__aisys__/api/operation-logs/${encodeURIComponent(id)}`, cookie)
  assert.equal(response.status, 200, `Go owner 管理详情应成功：${response.text}`)
  const data = envelope<OperationDetail>(response.text)
  assert.equal(data.operationKey, operationKey)
  assert.equal(data.resourceType, resourceType)
  return data
}

async function assertPersonalSummary(baseURL: string, cookie: string, id: string, visible: boolean): Promise<void> {
  const response = await request(baseURL, `/__aisys__/api/my-operation-logs/${encodeURIComponent(id)}`, cookie)
  assert.equal(response.status, visible ? 200 : 404, `个人详情可见性不符合 operation log contract：${response.text}`)
  if (!visible) return
  const data = envelope<{ changes?: unknown[]; targets?: unknown[]; viewers?: unknown[]; clientIp?: unknown }>(response.text)
  assert.deepEqual(data.changes, [], 'summary 个人详情不得展开完整变更')
  assert.deepEqual(data.targets, [], 'summary 个人详情 targets 必须保持数组 JSON 形状')
  assert.deepEqual(data.viewers, [], 'summary 个人详情 viewers 必须保持数组 JSON 形状')
  assert.equal(data.clientIp, undefined, '个人详情不得返回 clientIp')
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
  assert(address && typeof address === 'object', '测试服务监听地址无效')
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
