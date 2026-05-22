import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-runtime-snapshot-fallback-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'runtime-snapshot-fallback.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'runtime-snapshot-fallback-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { createSystemApiApp },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/system-api/system-api-app.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface AuditLogRuntimeResponse {
  runtimeAvailable: boolean
  workerSnapshotAvailable: boolean
  auditLogQueueAvailable: boolean
  queueLength: number | null
  droppedSuccessCount: number | null
  activeCaptureCount: number | null
  worker: {
    available: boolean
    ready: boolean | null
    pendingMessageCount: number | null
  }
}

interface RuntimeLogSearchResponse {
  runtimeAvailable: boolean
  workerSnapshotAvailable: boolean
  runtimeLogIndexQueueAvailable: boolean
  retentionDays: number | null
  retentionDaysSource: string
}

interface RuntimeLogFacetsResponse {
  runtimeAvailable: boolean
  workerSnapshotAvailable: boolean
  runtimeLogIndexQueueAvailable: boolean
  runtime: unknown
  worker: {
    available: boolean
    ready: boolean | null
    pendingMessageCount: number | null
  }
  dbService: {
    statusAvailable: boolean
    stateAvailable: boolean
    ready: boolean | null
    pendingRequestCount: number | null
  }
  gatewayAccountSideEffectsAvailable: boolean
  gatewayAccountSideEffects: unknown
}

interface SystemMetricsResponse {
  runtimeSnapshotAvailable: boolean
  workerSnapshotAvailable: boolean
  backgroundJobsAvailable: boolean
  backgroundJobs: unknown
  processEventLoopLatestStatus: Array<{
    processRole: string
    sampleAvailable: boolean
    processPid: number | null
    sampledAt: string | null
    eventLoopLagMs: number | null
  }>
  processEventLoopPeakStatus: Array<{
    processRole: string
    sampleAvailable: boolean
    processPid: number | null
    sampledAt: string | null
    eventLoopLagMs: number | null
  }>
}

interface AccountListResponse {
  items: Array<{
    id: string
    currentConcurrency: number
    currentConcurrencyAvailable?: boolean
  }>
  runtimeSnapshot: {
    accountConcurrencyAvailable: boolean
  }
}

interface GroupListResponse {
  items: Array<{
    id: string
    accountStats: {
      currentConcurrency: number
      currentConcurrencyAvailable?: boolean
    }
  }>
  runtimeSnapshot: {
    accountConcurrencyAvailable: boolean
  }
}

let server: http.Server | undefined

try {
  const seed = seedData()
  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api' })
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`

  const auditRuntime = await getEnvelope<AuditLogRuntimeResponse>(baseUrl, '/__aisys__/api/audit-logs/runtime', seed.adminCookie)
  assert.equal(auditRuntime.runtimeAvailable, false, '审计运行态应标记 server runtime 不可用')
  assert.equal(auditRuntime.workerSnapshotAvailable, false, '审计运行态应标记 worker snapshot 不可用')
  assert.equal(auditRuntime.auditLogQueueAvailable, false, '审计运行态应标记审计队列不可用')
  assert.equal(auditRuntime.queueLength, null, '审计队列不可用时 queueLength 不能伪装成 0')
  assert.equal(auditRuntime.droppedSuccessCount, null, '审计队列不可用时 droppedSuccessCount 不能伪装成 0')
  assert.equal(auditRuntime.activeCaptureCount, null, 'server runtime 不可用时 activeCaptureCount 不能伪装成 0')
  assert.equal(auditRuntime.worker.ready, null, 'worker 状态不可用时 ready 不能伪装成 false')
  assert.equal(auditRuntime.worker.pendingMessageCount, null, 'worker 状态不可用时 pendingMessageCount 不能伪装成 0')

  const runtimeLogSearch = await getEnvelope<RuntimeLogSearchResponse>(baseUrl, '/__aisys__/api/runtime-logs?limit=1', seed.adminCookie)
  assert.equal(runtimeLogSearch.runtimeAvailable, false, '运行日志列表应标记 server runtime 不可用')
  assert.equal(runtimeLogSearch.workerSnapshotAvailable, false, '运行日志列表应标记 worker snapshot 不可用')
  assert.equal(runtimeLogSearch.runtimeLogIndexQueueAvailable, false, '运行日志列表应标记索引队列不可用')
  assert.equal(runtimeLogSearch.retentionDays, null, 'worker snapshot 不可用时列表 retentionDays 不能伪装成默认 3')
  assert.equal(runtimeLogSearch.retentionDaysSource, 'unavailable', 'worker snapshot 不可用时应标记 retentionDays 来源不可用')

  const runtimeLogFacets = await getEnvelope<RuntimeLogFacetsResponse>(baseUrl, '/__aisys__/api/runtime-logs/facets', seed.adminCookie)
  assert.equal(runtimeLogFacets.runtimeAvailable, false, '运行日志 facets 应标记 server runtime 不可用')
  assert.equal(runtimeLogFacets.workerSnapshotAvailable, false, '运行日志 facets 应标记 worker snapshot 不可用')
  assert.equal(runtimeLogFacets.runtimeLogIndexQueueAvailable, false, '运行日志 facets 应标记索引队列不可用')
  assert.equal(runtimeLogFacets.runtime, null, '运行日志 runtime 不可用时不能伪装成空队列')
  assert.equal(runtimeLogFacets.worker.ready, null, 'worker 状态不可用时 facets ready 不能伪装成 false')
  assert.equal(runtimeLogFacets.worker.pendingMessageCount, null, 'worker 状态不可用时 facets pendingMessageCount 不能伪装成 0')
  assert.equal(runtimeLogFacets.dbService.statusAvailable, true, 'DB service 本地 status 仍应可用')
  assert.equal(runtimeLogFacets.dbService.stateAvailable, false, 'server runtime 缺失时 DB service 父进程状态应标记不可用')
  assert.equal(runtimeLogFacets.dbService.ready, true, 'DB service 本地 status 可用时 ready 应来自本地 status')
  assert.equal(runtimeLogFacets.gatewayAccountSideEffectsAvailable, false, '网关账户副作用运行态应标记不可用')
  assert.equal(runtimeLogFacets.gatewayAccountSideEffects, null, '网关账户副作用不可用时不能伪装成 0 队列')

  const systemMetrics = await getEnvelope<SystemMetricsResponse>(baseUrl, '/__aisys__/api/stats/system-metrics', seed.adminCookie)
  assert.equal(systemMetrics.runtimeSnapshotAvailable, false, '系统指标应标记 runtime snapshot 不可用')
  assert.equal(systemMetrics.workerSnapshotAvailable, false, '系统指标应标记 worker snapshot 不可用')
  assert.equal(systemMetrics.backgroundJobsAvailable, false, '后台任务不可用时应有显式标记')
  assert.equal(systemMetrics.backgroundJobs, null, '后台任务不可用时不能伪装成空数组')
  assert.deepEqual(
    systemMetrics.processEventLoopLatestStatus.map((item) => item.processRole),
    ['server', 'worker', 'db-service'],
    '系统指标应固定返回所有进程角色的采样可用性'
  )
  for (const item of systemMetrics.processEventLoopLatestStatus) {
    assert.equal(item.sampleAvailable, false, `${item.processRole} 无最新采样时应显式标记不可用`)
    assert.equal(item.processPid, null, `${item.processRole} 无最新采样时 PID 不能伪装成 0`)
    assert.equal(item.sampledAt, null, `${item.processRole} 无最新采样时采样时间不能伪装成默认值`)
    assert.equal(item.eventLoopLagMs, null, `${item.processRole} 无最新采样时事件循环延迟不能伪装成 0`)
  }
  assert.deepEqual(
    systemMetrics.processEventLoopPeakStatus.map((item) => item.processRole),
    ['server', 'worker', 'db-service'],
    '系统指标应固定返回所有进程角色的 24 小时峰值可用性'
  )
  for (const item of systemMetrics.processEventLoopPeakStatus) {
    assert.equal(item.sampleAvailable, false, `${item.processRole} 无最近 24 小时峰值采样时应显式标记不可用`)
    assert.equal(item.processPid, null, `${item.processRole} 无最近 24 小时峰值采样时 PID 不能伪装成 0`)
    assert.equal(item.sampledAt, null, `${item.processRole} 无最近 24 小时峰值采样时采样时间不能伪装成默认值`)
    assert.equal(item.eventLoopLagMs, null, `${item.processRole} 无最近 24 小时峰值采样时事件循环延迟不能伪装成 0`)
  }

  const accountPage = await getEnvelope<AccountListResponse>(baseUrl, '/__aisys__/api/accounts?page=1&pageSize=20', seed.adminCookie)
  assert.equal(accountPage.runtimeSnapshot.accountConcurrencyAvailable, false, '账户分页应标记实时并发快照不可用')
  const account = accountPage.items.find((item) => item.id === seed.accountId)
  assert(account, '测试账户应出现在账户列表')
  assert.equal(account.currentConcurrency, 0, '快照不可用时 currentConcurrency 保留仓库默认值')
  assert.equal(account.currentConcurrencyAvailable, false, '实时并发快照不可用时账户不应展示 currentConcurrency 为真实值')

  const groupPage = await getEnvelope<GroupListResponse>(baseUrl, '/__aisys__/api/groups?page=1&pageSize=20', seed.adminCookie)
  assert.equal(groupPage.runtimeSnapshot.accountConcurrencyAvailable, false, '分组分页应标记实时并发快照不可用')
  const group = groupPage.items.find((item) => item.id === seed.groupId)
  assert(group, '测试分组应出现在分组列表')
  assert.equal(group.accountStats.currentConcurrency, 0, '快照不可用时分组 currentConcurrency 保留仓库默认值')
  assert.equal(group.accountStats.currentConcurrencyAvailable, false, '实时并发快照不可用时分组不应展示 currentConcurrency 为真实值')

  console.log('运行态快照不可用契约回归通过：API 不再把 unknown 伪装成 0、false、[] 或默认天数')
} finally {
  await closeServer(server)
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedData(): { accountId: string; adminCookie: string; groupId: string } {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  const access = { systemAccountId: admin.id, role: 'admin' as const }
  const group = repositories.createGroup({
    name: '运行态快照不可用分组',
    providerCode: 'openai'
  }, access)
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '运行态快照不可用账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-runtime-snapshot-fallback',
      base_url: 'http://127.0.0.1:9/v1'
    },
    status: 'active',
    concurrencyLimit: 10,
    schedulable: true,
    groupId: group.id
  }, access)
  return {
    accountId: account.id,
    adminCookie: `juhe_ai_session=${repositories.createSession(admin.id, 1).token}`,
    groupId: group.id
  }
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
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
    listeningServer.close((error) => {
      if (error) {
        rejectPromise(error)
      } else {
        resolvePromise()
      }
    })
  })
}

function serverAddress(listeningServer: http.Server): { port: number } {
  const address = listeningServer.address()
  assert(address && typeof address !== 'string', '测试服务器应监听 TCP 地址')
  return { port: address.port }
}
