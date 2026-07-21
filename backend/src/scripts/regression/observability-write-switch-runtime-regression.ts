import assert from 'node:assert/strict'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

process.env.JUHE_AI_AUDIT_LOG_ENABLED = 'false'
process.env.JUHE_AI_RUNTIME_LOG_INDEX_ENABLED = 'false'

const { runtimeConfig } = await import('../../config/runtime.js')
const tempRoot = resolve(tmpdir(), `juhe-ai-observability-disabled-runtime-${Date.now()}-${Math.random().toString(16).slice(2)}`)
mkdirSync(tempRoot, { recursive: true })
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.cacheDriver = 'memory'
runtimeConfig.secret = 'observability-disabled-runtime-secret'
runtimeConfig.processRole = 'db-service'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false

const [{ createSystemApiApp }, database, repositories, auditSettings] = await Promise.all([
  import('../../modules/system-api/system-api-app.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/audit-logs/audit-log-settings.js')
])

interface ApiEnvelope<T> {
  data: T
}

interface AuditRuntime {
  enabled: boolean
  unavailableReason?: string
  runtimeAvailable: boolean
  worker: { available: boolean }
}

interface RuntimeLogRuntime {
  indexEnabled: boolean
  unavailableReason?: string
  runtimeAvailable: boolean
  ingestWorkerAvailable: boolean
  runtimeLogIndexQueueAvailable: boolean
  dbService: { statusAvailable: boolean; stateAvailable: boolean }
}

let server: http.Server | undefined

try {
  database.getDatasetDatabase()
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const cookie = `juhe_ai_session=${repositories.createSession(admin.id, 1).token}`

  const app = createSystemApiApp({
    systemApiPrefix: '/__aisys__/api',
    bypassSystemApiRateLimitForTest: true
  })
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`

  const auditRuntime = await getEnvelope<AuditRuntime>(baseUrl, '/__aisys__/api/audit-logs/runtime', cookie)
  assert.equal(auditSettings.readAuditLogSettings().enabled, false)
  assert.equal(auditRuntime.enabled, false)
  assert.equal(auditRuntime.unavailableReason, 'audit_disabled')
  assert.equal(auditRuntime.runtimeAvailable, false, '禁用原因不得伪造 server runtime 可用')

  const runtimeLogRuntime = await getEnvelope<RuntimeLogRuntime>(baseUrl, '/__aisys__/api/runtime-logs/runtime', cookie)
  assert.equal(runtimeConfig.log.indexEnabled, false)
  assert.equal(runtimeLogRuntime.indexEnabled, false)
  assert.equal(runtimeLogRuntime.unavailableReason, 'index_disabled')
  assert.equal(runtimeLogRuntime.runtimeLogIndexQueueAvailable, false)
  assert.equal(runtimeLogRuntime.dbService.statusAvailable, true, '索引关闭不得误报当前 DB service health')
  assert.equal(runtimeLogRuntime.runtimeAvailable, false, '索引关闭不得伪造父进程 runtime 快照')

  console.log('observability disabled runtime regression passed')
} finally {
  await closeServer(server)
  const { closeSqliteReadWorkerPool } = await import('../../storage/sqlite-read-worker-pool.js')
  await closeSqliteReadWorkerPool().catch(() => undefined)
  database.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  const body = await response.text()
  if (!response.ok) throw new Error(`${path} HTTP ${response.status}: ${body}`)
  return (JSON.parse(body) as ApiEnvelope<T>).data
}

async function listen(listeningServer: http.Server): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: http.Server): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

function serverAddress(listeningServer: http.Server): { port: number } {
  const address = listeningServer.address()
  assert(address && typeof address !== 'string', '测试服务器应监听 TCP 地址')
  return { port: address.port }
}

