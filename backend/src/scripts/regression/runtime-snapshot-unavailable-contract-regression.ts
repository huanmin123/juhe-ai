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
const systemMetricsRouteSource = statsRoutesSource.match(/statsRouter\.get\('\/system-metrics',[\s\S]*?\n\}\)\n/)?.[0]
const systemMetricsRuntimeRouteSource = statsRoutesSource.match(/statsRouter\.get\('\/system-metrics\/runtime',[\s\S]*?\n\}\)\n/)?.[0]
assert(systemMetricsRouteSource, '应定义系统指标首屏路由')
assert(systemMetricsRuntimeRouteSource, '应定义系统指标运行态独立路由')
assert.doesNotMatch(systemMetricsRouteSource, /requestServerRuntimeSnapshot|get[A-Za-z]+RedisStreamRuntime|backgroundQueueRuntimeRows/, '首屏趋势路由不得采集 server / Redis 运行态')
assert.match(systemMetricsRuntimeRouteSource, /requestServerSystemMetricsRuntimeSnapshot\(2500\)/, '独立运行态路由快照预算必须大于内部 worker 快照预算')
assert.match(dbServiceIpcSource, /requestIngestWorkerSnapshot\(1500\)/)
assert.match(dbServiceIpcSource, /requestStatsWorkerSnapshot\(1500\)/)
assert.match(dbServiceIpcSource, /requestOpsWorkerSnapshot\(1500\)/)
assert.match(statsRoutesSource, /consumers: optionalNumberValue\(queue\.consumers\)/, '缺失 consumers 不能被伪造成 0')
assert.match(statsRoutesSource, /expiredCount: numberValue\(state\.expiredCount\)/, '网关过期必须与丢弃分别展示')
assert.match(dbServiceIpcSource, /observedAt: new Date\(\)\.toISOString\(\)/, '运行态快照必须记录采集时间')
assert.match(statsRoutesSource, /const runtimeSnapshotAgeMs/, '系统指标接口必须计算快照年龄用于过期判断')
assert.doesNotMatch(systemMetricsRuntimeRouteSource, /runtimeSnapshotAgeMs,/, '系统指标接口不得返回页面未展示的快照年龄')
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
  enabled: boolean
  unavailableReason?: string
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
  items: Array<Record<string, unknown>>
  hasMore: boolean
  page: number
  pageSize: number
  total: number
}

interface RuntimeLogDetailDeltaResponse {
  id: string
  rawJson: string
}

interface RuntimeLogFacetsResponse {
  retentionDays: number
  earliestIndexedAt?: string
  latestIndexedAt?: string
  totalIndexed: number
  levels: Array<{ value: string; count: number }>
  events: string[]
}

interface SystemMetricsResponse {
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

interface SystemMetricsRuntimeResponse {
  runtimeSnapshotAvailable: boolean
  ingestWorkerSnapshotAvailable: boolean
  statsWorkerSnapshotAvailable: boolean
  opsWorkerSnapshotAvailable: boolean
  backgroundJobsAvailable: boolean
  backgroundJobs: unknown
}

interface AccountListResponse {
  items: Array<{
    id: string
    currentConcurrency: number
    effectiveAvailability: { label: string }
  }>
}

interface GroupListResponse {
  items: Array<{
    id: string
    accountStats: Record<string, unknown>
  }>
  runtimeSnapshot?: {
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
  assert.equal(auditRuntime.enabled, true, '未配置总开关时审计运行态必须标记启用')
  assert.equal(auditRuntime.unavailableReason, 'server_runtime_unavailable', '启用态必须保留原有运行态不可用原因')
  assert.equal(auditRuntime.runtimeAvailable, false, '审计运行态应标记 server runtime 不可用')
  assert.equal(auditRuntime.workerSnapshotAvailable, false, '审计运行态应标记 worker snapshot 不可用')
  assert.equal(auditRuntime.auditLogQueueAvailable, false, '审计运行态应标记审计队列不可用')
  assert.equal(auditRuntime.queueLength, null, '审计队列不可用时 queueLength 不能伪装成 0')
  assert.equal(auditRuntime.droppedSuccessCount, null, '审计队列不可用时 droppedSuccessCount 不能伪装成 0')
  assert.equal(auditRuntime.activeCaptureCount, null, 'server runtime 不可用时 activeCaptureCount 不能伪装成 0')
  assert.equal(auditRuntime.worker.ready, null, 'worker 状态不可用时 ready 不能伪装成 false')
  assert.equal(auditRuntime.worker.pendingMessageCount, null, 'worker 状态不可用时 pendingMessageCount 不能伪装成 0')

  const runtimeLogSearch = await getEnvelope<RuntimeLogSearchResponse>(baseUrl, '/__aisys__/api/runtime-logs?page=1&pageSize=1', seed.adminCookie)
  assert.deepEqual(
    Object.keys(runtimeLogSearch).sort(),
    ['hasMore', 'items', 'page', 'pageSize', 'total'].sort(),
    '运行日志列表只应返回分页数据，不得夹带运行态或保留期'
  )
  assert.equal(runtimeLogSearch.items[0]?.id, seed.runtimeLogId, '运行日志列表应返回测试摘要行')
  assert(!('rawJson' in runtimeLogSearch.items[0]!), '运行日志列表响应不得提前返回 rawJson')
  assert(!('createdAt' in runtimeLogSearch.items[0]!), '运行日志列表响应不得返回未消费 createdAt')
  const runtimeLogDetail = await getEnvelope<RuntimeLogDetailDeltaResponse>(baseUrl, `/__aisys__/api/runtime-logs/${seed.runtimeLogId}`, seed.adminCookie)
  assert.deepEqual(Object.keys(runtimeLogDetail).sort(), ['id', 'rawJson'], '运行日志详情 HTTP 响应只应补充 id + rawJson')
  assert.match(runtimeLogDetail.rawJson, /runtime_snapshot_detail_delta/, '运行日志详情增量应返回完整原文')

  const runtimeLogFacets = await getEnvelope<RuntimeLogFacetsResponse>(baseUrl, '/__aisys__/api/runtime-logs/facets', seed.adminCookie)
  assert.equal(typeof runtimeLogFacets.retentionDays, 'number', '运行日志 facets 必须返回保留天数')
  assert.equal(typeof runtimeLogFacets.totalIndexed, 'number', '运行日志 facets 必须返回索引总数')
  assert(Array.isArray(runtimeLogFacets.levels), '运行日志 facets 必须返回级别选项')
  assert(Array.isArray(runtimeLogFacets.events), '运行日志 facets 必须返回事件选项')
  const expectedRuntimeLogFacetKeys = [
    'events',
    'levels',
    'retentionDays',
    'totalIndexed'
  ]
  if ('earliestIndexedAt' in runtimeLogFacets) expectedRuntimeLogFacetKeys.push('earliestIndexedAt')
  if ('latestIndexedAt' in runtimeLogFacets) expectedRuntimeLogFacetKeys.push('latestIndexedAt')
  assert.deepEqual(
    Object.keys(runtimeLogFacets).sort(),
    expectedRuntimeLogFacetKeys.sort(),
    '运行日志 facets 只应返回筛选与范围信息，不得夹带进程、队列和 DB service 运行态'
  )
  const runtimeLogGrepOptions = await getEnvelope<Record<string, unknown>>(baseUrl, '/__aisys__/api/runtime-logs/grep-options', seed.adminCookie)
  assert('defaultRangeDays' in runtimeLogGrepOptions, 'grep 模式应通过独立接口读取文件时间范围')

  const systemMetricsRuntime = await getEnvelope<SystemMetricsRuntimeResponse>(baseUrl, '/__aisys__/api/stats/system-metrics/runtime', seed.adminCookie)
  assert.equal(systemMetricsRuntime.runtimeSnapshotAvailable, false, '系统指标应标记 runtime snapshot 不可用')
  assert.equal(systemMetricsRuntime.ingestWorkerSnapshotAvailable, false, '系统指标应标记 ingest-worker snapshot 不可用')
  assert.equal(systemMetricsRuntime.statsWorkerSnapshotAvailable, false, '系统指标应标记 stats-worker snapshot 不可用')
  assert.equal(systemMetricsRuntime.opsWorkerSnapshotAvailable, false, '系统指标应标记 ops-worker snapshot 不可用')
  assert.equal(systemMetricsRuntime.backgroundJobsAvailable, false, '后台任务不可用时应有显式标记')
  assert.equal(systemMetricsRuntime.backgroundJobs, null, '后台任务不可用时不能伪装成空数组')
  const systemMetrics = await getEnvelope<SystemMetricsResponse>(baseUrl, '/__aisys__/api/stats/system-metrics', seed.adminCookie)
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
  const account = accountPage.items.find((item) => item.id === seed.accountId)
  assert(account, '测试账户应出现在账户列表')
  assert.equal(account.currentConcurrency, 0, '运行态读取不可用时账户列表并发应返回 0')
  assert.equal(account.effectiveAvailability.label, '可调度', '账户列表必须返回综合后的真实调度状态')

  const groupPage = await getEnvelope<GroupListResponse>(baseUrl, '/__aisys__/api/groups?page=1&pageSize=20', seed.adminCookie)
  const group = groupPage.items.find((item) => item.id === seed.groupId)
  assert(group, '测试分组应出现在分组列表')
  assert.equal(group.accountStats.currentConcurrency, 0, '运行态读取不可用时分组列表并发应返回 0')
  assert.equal(typeof group.accountStats.todayUsage, 'object', '分组列表必须内联当日用量')

  console.log('列表完整响应契约回归通过：运行态无占用或读取不可用时并发返回 0，状态和用量随列表返回')
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

function seedData(): { accountId: string; adminCookie: string; groupId: string; runtimeLogId: string } {
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
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5',
    healthCheckEndpointMode: 'responses_sse',
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
  const runtimeLogId = 'rtlog_runtime_snapshot_detail_delta'
  const runtimeLogTime = new Date().toISOString()
  repositories.createRuntimeLogsBatch([{
    id: runtimeLogId,
    time: runtimeLogTime,
    level: 'info',
    traceId: 'trace-runtime-snapshot-detail-delta',
    event: 'runtime_snapshot_detail_delta',
    message: 'runtime snapshot detail delta',
    rawJson: JSON.stringify({ event: 'runtime_snapshot_detail_delta', time: runtimeLogTime }),
    createdAt: runtimeLogTime
  }])
  usageStatsRepository.refreshDirtyGroupAccountStatsCache()
  return {
    accountId: account.id,
    adminCookie: `juhe_ai_session=${repositories.createSession(admin.id, 1).token}`,
    groupId: group.id,
    runtimeLogId
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
