import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { backgroundWorkerRegistry } from '../../modules/background/background-job-registry.js'

const supervisorSource = readSource('../../modules/background/background-worker-supervisor.ts')
const serverSource = readSource('../../server.ts')
const backgroundJobsSource = readSource('../../modules/background/background-jobs.ts')
const workerSource = readSource('../../worker.ts')
const runtimeSource = readSource('../../config/runtime.ts')
const processMonitorSource = readSource('../../shared/process-event-loop-monitor.ts')
const systemMetricsSource = readSource('../../storage/system-metrics.repository.ts')
const statsRoutesSource = readSource('../../modules/stats/stats.routes.ts')
const dbServiceIpcSource = readSource('../../modules/db-service/db-service-ipc.ts')
const frontendBackgroundJobsSource = readSource('../../../../frontend/src/views/stats/StatsBackgroundJobsCard.vue')

const registryByName = new Map<string, typeof backgroundWorkerRegistry[number]>(backgroundWorkerRegistry.map((job) => [job.jobName, job]))
const expectedSupervisedRoles = [
  'worker',
  'metrics-worker',
  'ingest-worker',
  'stats-worker',
  'snapshot-worker',
  'probe-worker',
  'maintenance-worker'
] as const

assert(runtimeSource.includes("workerRole: workerRoleConfig('JUHE_AI_WORKER_ROLE', 'worker')"), 'runtimeConfig 必须支持 JUHE_AI_WORKER_ROLE')
for (const role of expectedSupervisedRoles) {
  assert(supervisorSource.includes(`'${role}'`), `supervisor 必须声明 ${role}`)
}
assert(supervisorSource.includes("JUHE_AI_WORKER_ROLE: role"), 'supervisor fork 子进程时必须传入 worker role')
assert(supervisorSource.includes('attachBackgroundWorkerProcess(child, {') && supervisorSource.includes('role,'), 'supervisor attach worker IPC 时必须传入 role')
assert(supervisorSource.includes('startWorkerProcessesInSequence()'), 'supervisor 首次启动必须按序启动 worker，避免多个 worker 同时初始化 SQLite')
assert(supervisorSource.includes('workerStartupReadyTimeoutMs'), 'supervisor 按序启动 worker 时必须有 ready 等待上限，避免单个 worker 阻塞后续角色')
assert(serverSource.includes('startDbServiceSupervisor({ onReady: startBackgroundWorkerSupervisorAfterDbServiceReady })'), 'server 必须在 DB service ready 后启动后台 worker，避免 DB service 与首个 worker 同时初始化 SQLite')
assert(serverSource.includes('backgroundWorkerStartupFallbackMs'), 'server 等待 DB service ready 必须有兜底超时，避免 DB service 异常时 worker 永久不启动')
assert(!serverSource.includes('startDbServiceSupervisor()\nstartBackgroundWorkerSupervisor()'), 'server 不能背靠背启动 DB service 和后台 worker')

assert(backgroundJobsSource.includes("case 'metrics-worker':"), 'background-jobs 必须按 workerRole 保留 metrics-worker 分支')
assertRoleBlockContainsOnly('metrics-worker', [])
assertRoleBlockContainsOnly('ingest-worker', ['api-key-record-cleanup-retry', 'account-record-cleanup-retry', 'audit-hot-retention-cleanup', 'data-retention-cleanup', 'runtime-log-index-maintenance'])
assertRoleBlockContainsOnly('stats-worker', [
  'system-metrics-sample',
  'usage-stats-aggregation',
  'client-ip-stats-aggregation',
  'group-account-stats-refresh',
  'usage-rank-snapshots-refresh',
  'system-metrics-trend-windows-refresh',
  'usage-overview-windows-refresh',
  'usage-scope-range-windows-refresh',
  'authorization-usage-range-windows-refresh',
  'account-quality-refresh',
  'table-storage-monitor',
  'usage-stats-consistency-check'
])
assertRoleBlockContainsOnly('snapshot-worker', [])
assertRoleBlockContainsOnly('probe-worker', ['proxy-latency-refresh', 'account-health-check', 'cooldown-account-retest', 'account-api-key-cooldown-retest', 'openai-oauth-access-token-refresh'])
assertRoleBlockContainsOnly('maintenance-worker', ['api-key-availability-schedule-status-sync', 'account-availability-schedule-status-sync', 'resource-authorization-expiry-sweep', 'expired-deleted-account-cleanup'])
assert(!backgroundJobsSource.includes("buildProcessEventLoopSample('worker')"), 'system-metrics-sample 已在 metrics-worker 内运行，不能把本地事件循环样本硬编码成 worker')
assert(backgroundJobsSource.includes('const localProcessEventLoopSample = buildProcessEventLoopSample()'), 'system-metrics-sample 本地事件循环样本必须使用当前 workerRole')

assert(workerSource.includes('if (isIngestWorker()) {'), 'worker.ts 必须把 append-only 写入队列隔离到 ingest-worker')
assert(workerSource.includes('} else if (isMaintenanceWorker()) {'), 'maintenance-worker 必须独立启动维护本地队列')
assert(workerSource.includes('} else if (isProbeWorker()) {'), 'probe-worker 必须独立启动账号测试和探测本地队列')
assert(workerSource.includes('startRuntimeLogFileImport()'), 'ingest-worker 应启动运行日志文件导入')
assert(workerSource.includes('startAccountTestTaskQueue()'), 'probe-worker 应启动手动账号测试队列')
assert(workerSource.includes('getAccountApiKeyCooldownRetestQueueSnapshot') && workerSource.includes('accountApiKeyCooldownRetestQueue'), 'probe-worker runtime snapshot 必须暴露 Key 级冷却复测队列')
assert(statsRoutesSource.includes("job.name === 'account-api-key-cooldown-retest'") && statsRoutesSource.includes('probeWorkerSnapshot.accountApiKeyCooldownRetestQueue'), '系统指标后台任务表必须把 Key 级冷却复测队列挂到对应任务行')
assert(statsRoutesSource.includes("retryQueueBackgroundJobRow('manual-account-test-queue'") && statsRoutesSource.includes('probeWorkerSnapshot?.manualAccountTestQueue'), '系统指标后台任务表必须展示手动账号测试本地队列')
assert(statsRoutesSource.includes("retryQueueBackgroundJobRow('account-quality-failure-precheck-queue'") && statsRoutesSource.includes('probeWorkerSnapshot?.accountQualityFailurePrecheckQueue'), '系统指标后台任务表必须展示账号质量失败预检队列')
assert(statsRoutesSource.includes("localQueueBackgroundJobRow('record-maintenance-ingest-queue'") && statsRoutesSource.includes('ingestWorkerSnapshot?.recordMaintenanceQueue'), '系统指标后台任务表必须展示 ingest-worker 数据维护本地队列')
assert(statsRoutesSource.includes("localQueueBackgroundJobRow('record-maintenance-stats-queue'") && statsRoutesSource.includes('statsWorkerSnapshot?.recordMaintenanceQueue'), '系统指标后台任务表必须展示 stats-worker 数据维护本地队列')
assert(dbServiceIpcSource.includes('accountApiKeyCooldownRetestQueue: probeWorkerSnapshot.accountApiKeyCooldownRetestQueue'), 'DB service runtime snapshot 必须转发 probe-worker 的 Key 级冷却复测队列')
assert(dbServiceIpcSource.includes('recordMaintenanceQueue: { ...ingestWorkerSnapshot.recordMaintenanceQueue }'), 'DB service runtime snapshot 必须转发 ingest-worker 数据维护本地队列')
assert(dbServiceIpcSource.includes('recordMaintenanceQueue: { ...statsWorkerSnapshot.recordMaintenanceQueue }'), 'DB service runtime snapshot 必须转发 stats-worker 数据维护本地队列')
assert(frontendBackgroundJobsSource.includes('const queue = row.retryQueue'), '前端后台任务表必须展示任意任务行携带的 retryQueue，不能只显示账号级冷却复测')
assert(frontendBackgroundJobsSource.includes('const queue = row.localQueue'), '前端后台任务表必须展示任意任务行携带的 localQueue，不能漏掉数据维护本地队列')
assert(workerSource.includes('isIngestWorkerMessage'), 'worker.ts 必须禁止默认 worker 消费 ingest-worker 消息')
assert(workerSource.includes('isProbeWorkerMessage'), 'worker.ts 必须禁止非 probe-worker 消费探测消息')
assert(workerSource.includes('isMaintenanceWorkerMessage'), 'worker.ts 必须禁止非 maintenance-worker 消费维护消息')
assert(workerSource.includes("message.type === 'background_worker_process_event_loop_request'"), 'worker 必须响应 server 发起的事件循环采样请求')

assert(processMonitorSource.includes("runtimeConfig.workerRole"), '事件循环采样必须使用 workerRole 区分 metrics-worker')
for (const role of expectedSupervisedRoles) {
  assert(systemMetricsSource.includes(`'${role}'`), `系统指标角色清单必须包含 ${role}`)
}

const metricsJob = registryByName.get('system-metrics-sample')
assert(metricsJob, 'system-metrics-sample 必须登记到 registry')
assert.equal(metricsJob.defaultRole, 'stats-worker', 'system-metrics-sample 默认角色必须是 stats-worker，避免 metrics-worker 抢写 stats 库')
assert.equal(metricsJob.lifecycle, 'persistent', 'system-metrics-sample 必须是持久 worker 任务')

for (const job of backgroundWorkerRegistry) {
  if (job.category !== 'scheduled') {
    continue
  }
  assert.notEqual(job.defaultRole, 'metrics-worker', `${job.jobName} 不应默认挂到 metrics-worker`)
}

console.log('worker 角色回归通过：stats 写任务集中到 stats-worker，metrics/snapshot worker 不再抢写 stats 库')

function readSource(path: string): string {
  return readFileSync(new URL(path, import.meta.url), 'utf8').replace(/\r\n/g, '\n')
}

function assertRoleBlockContainsOnly(role: string, jobNames: string[]): void {
  const block = roleCaseBlock(role)
  for (const jobName of jobNames) {
    assert(block.includes(`backgroundScheduledJobName('${jobName}')`), `${role} 必须注册 ${jobName}`)
  }
  const scheduledNames = [...new Set([...block.matchAll(/backgroundScheduledJobName\('([^']+)'\)/g)].map((match) => match[1]))]
  assert.deepEqual(scheduledNames, jobNames, `${role} 注册任务必须和当前角色归属一致`)
  for (const jobName of jobNames) {
    assert.equal(registryByName.get(jobName)?.defaultRole, role, `${jobName} registry defaultRole 必须和 ${role} 实际挂载一致`)
  }
}

function roleCaseBlock(role: string): string {
  const marker = `case '${role}':`
  const start = backgroundJobsSource.indexOf(marker)
  assert(start >= 0, `background-jobs 必须包含 ${marker}`)
  const rest = backgroundJobsSource.slice(start + marker.length)
  const nextCase = rest.search(/\n\s*case\s+'[^']+':|\n\s*default:/)
  return nextCase >= 0 ? rest.slice(0, nextCase) : rest
}
