import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-runtime-snapshot-unavailable-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'runtime-snapshot-unavailable.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'runtime-snapshot-unavailable-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const statsRoutesSource = readFileSync(resolve('src/modules/stats/stats.routes.ts'), 'utf8')
const dbServiceIpcSource = readFileSync(resolve('src/modules/db-service/db-service-ipc.ts'), 'utf8')
const workerSchedulerSource = readFileSync(resolve('src/modules/background/worker-scheduler.ts'), 'utf8')
assert.match(statsRoutesSource, /requestServerRuntimeSnapshot\(2500\)/, '外层运行态快照预算必须大于内部 worker 快照预算')
assert.match(dbServiceIpcSource, /requestIngestWorkerSnapshot\(1500\)/)
assert.match(dbServiceIpcSource, /requestStatsWorkerSnapshot\(1500\)/)
assert.match(dbServiceIpcSource, /requestOpsWorkerSnapshot\(1500\)/)
assert.match(statsRoutesSource, /consumers: optionalNumberValue\(queue\.consumers\)/, '缺失 consumers 不能被伪造成 0')
assert.match(statsRoutesSource, /expiredCount: numberValue\(state\.expiredCount\)/, '网关过期必须与丢弃分别展示')
assert.match(dbServiceIpcSource, /observedAt: new Date\(\)\.toISOString\(\)/, '运行态快照必须记录采集时间')
assert.match(statsRoutesSource, /runtimeSnapshotAgeMs/, '系统指标接口必须返回运行态快照年龄')
assert.match(statsRoutesSource, /runtimeSnapshotStale/, '系统指标接口必须明确快照过期状态')
assert.doesNotMatch(workerSchedulerSource, /state\.lastErrorAt = undefined/, '任务成功后必须保留最近失败时间供恢复状态判断')

const [
  { createSystemApiApp },
  databaseModule,
  repositories,
  usageStatsRepository
] = await Promise.all([
  import('../../modules/system-api/system-api-app.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js')
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
  queueHealth: {
    available: boolean
    workerSnapshotAvailable: boolean
    serverIpcQueueAvailable: boolean
    status: string
    summary: {
      unavailableCount: number
      droppedCount: number
      rejectedCount: number
    }
  }
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
  ingestWorkerSnapshotAvailable: boolean
  statsWorkerSnapshotAvailable: boolean
  opsWorkerSnapshotAvailable: boolean
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
  // Read-only query workers require the dataset and stats files to exist first.
  databaseModule.getDatasetDatabase()
  databaseModule.getStatsDatabase()
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
  assert.equal(runtimeLogFacets.queueHealth.available, false, '队列健康快照应标记 server runtime 不可用')
  assert.equal(runtimeLogFacets.queueHealth.workerSnapshotAvailable, false, '队列健康快照应标记 worker snapshot 不可用')
  assert.equal(runtimeLogFacets.queueHealth.serverIpcQueueAvailable, false, '队列健康快照应标记 server IPC 队列不可用')
  assert.equal(runtimeLogFacets.queueHealth.status, 'unavailable', '运行态不可用时队列健康状态不能伪装成 normal')
  assert(runtimeLogFacets.queueHealth.summary.unavailableCount > 0, '运行态不可用时队列健康应记录不可用队列数量')
  assert.equal(runtimeLogFacets.queueHealth.summary.droppedCount, 0, '不可用不应伪造 dropped 指标')
  assert.equal(runtimeLogFacets.queueHealth.summary.rejectedCount, 0, '不可用不应伪造 rejected 指标')
  assert.equal(runtimeLogFacets.dbService.statusAvailable, true, 'DB service 本地 status 仍应可用')
  assert.equal(runtimeLogFacets.dbService.stateAvailable, false, 'server runtime 缺失时 DB service 父进程状态应标记不可用')
  assert.equal(runtimeLogFacets.dbService.ready, true, 'DB service 本地 status 可用时 ready 应来自本地 status')
  assert.equal(runtimeLogFacets.gatewayAccountSideEffectsAvailable, false, '网关账户副作用运行态应标记不可用')
  assert.equal(runtimeLogFacets.gatewayAccountSideEffects, null, '网关账户副作用不可用时不能伪装成 0 队列')

  const systemMetrics = await getEnvelope<SystemMetricsResponse>(baseUrl, '/__aisys__/api/stats/system-metrics', seed.adminCookie)
  assert.equal(systemMetrics.runtimeSnapshotAvailable, false, '系统指标应标记 runtime snapshot 不可用')
  assert.equal(systemMetrics.ingestWorkerSnapshotAvailable, false, '系统指标应标记 ingest-worker snapshot 不可用')
  assert.equal(systemMetrics.statsWorkerSnapshotAvailable, false, '系统指标应标记 stats-worker snapshot 不可用')
  assert.equal(systemMetrics.opsWorkerSnapshotAvailable, false, '系统指标应标记 ops-worker snapshot 不可用')
  assert.equal(systemMetrics.backgroundJobsAvailable, false, '后台任务不可用时应有显式标记')
  assert.equal(systemMetrics.backgroundJobs, null, '后台任务不可用时不能伪装成空数组')
  assert.deepEqual(
    systemMetrics.processEventLoopLatestStatus.map((item) => item.processRole),
    ['server', 'ingest-worker', 'stats-worker', 'ops-worker', 'db-service'],
    '系统指标应固定返回当前进程角色的采样可用性'
  )
  for (const item of systemMetrics.processEventLoopLatestStatus) {
    assert.equal(item.sampleAvailable, false, `${item.processRole} 无最新采样时应显式标记不可用`)
    assert.equal(item.processPid, null, `${item.processRole} 无最新采样时 PID 不能伪装成 0`)
    assert.equal(item.sampledAt, null, `${item.processRole} 无最新采样时采样时间不能伪装成默认值`)
    assert.equal(item.eventLoopLagMs, null, `${item.processRole} 无最新采样时事件循环延迟不能伪装成 0`)
  }
  assert.deepEqual(
    systemMetrics.processEventLoopPeakStatus.map((item) => item.processRole),
    ['server', 'ingest-worker', 'stats-worker', 'ops-worker', 'db-service'],
    '系统指标应固定返回当前进程角色的 24 小时峰值可用性'
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
  const { closeSqliteReadWorkerPool } = await import('../../storage/sqlite-read-worker-pool.js')
  await closeSqliteReadWorkerPool().catch(() => undefined)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedData(): { accountId: string; adminCookie: string; groupId: string } {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const access = { systemAccountId: admin.id, role: 'admin' as const }
  const group = repositories.createGroup({
    name: '运行态快照不可用分组',
    providerCode: 'gpt'
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '运行态快照不可用账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-runtime-snapshot-unavailable',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    concurrencyLimit: 10,
    schedulable: true,
    groupId: group.id
  }, access)
  assert.equal(repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), true, '运行态契约 fixture 应显式通过后台健康检查激活账户')
  usageStatsRepository.refreshDirtyGroupAccountStatsCache()
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
