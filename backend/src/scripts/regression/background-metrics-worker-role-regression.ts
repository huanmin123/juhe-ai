import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { backgroundWorkerRegistry } from '../../modules/background/background-job-registry.js'

const supervisorSource = readSource('../../modules/background/background-worker-supervisor.ts')
const serverSource = readSource('../../server.ts')
const backgroundJobsSource = readSource('../../modules/background/background-jobs.ts')
const backgroundIpcWorkerRolesSource = readSource('../../modules/background/background-ipc-worker-roles.ts')
const workerSource = readSource('../../worker.ts')
const runtimeSource = readSource('../../config/runtime.ts')
const processMonitorSource = readSource('../../shared/process-event-loop-monitor.ts')
const systemMetricsSource = readSource('../../storage/system-metrics.repository.ts')
const statsRoutesSource = readSource('../../modules/stats/stats.routes.ts')
const dbServiceIpcSource = readSource('../../modules/db-service/db-service-ipc.ts')
const frontendBackgroundJobsSource = readSource('../../../../frontend/src/views/stats/StatsBackgroundJobsCard.vue')

const registryByName = new Map<string, typeof backgroundWorkerRegistry[number]>(backgroundWorkerRegistry.map((job) => [job.jobName, job]))
const expectedSupervisedRoles = ['ingest-worker', 'stats-worker', 'ops-worker'] as const
const retiredRoles = ['metrics-worker', 'snapshot-worker', 'probe-worker', 'maintenance-worker'] as const

assert(runtimeSource.includes("workerRole: workerRoleConfig('JUHE_AI_WORKER_ROLE', 'worker')"), 'runtimeConfig 必须支持 JUHE_AI_WORKER_ROLE')
for (const role of expectedSupervisedRoles) {
  assert(supervisorSource.includes(`'${role}'`), `supervisor 必须声明 ${role}`)
}
for (const role of retiredRoles) {
  assert(!roleCaseExists(role), `background-jobs 不应保留 ${role} 独立调度分支`)
}
assert(supervisorSource.includes("JUHE_AI_WORKER_ROLE: role"), 'supervisor fork 子进程时必须传入 worker role')
assert(supervisorSource.includes('attachBackgroundWorkerProcess(child, {') && supervisorSource.includes('role,'), 'supervisor attach worker IPC 时必须传入 role')
assert(supervisorSource.includes('startWorkerProcessesInSequence()'), 'supervisor 首次启动必须按序启动 worker，避免多个 worker 同时初始化 SQLite')
assert(serverSource.includes('startDbServiceSupervisor({ onReady: startBackgroundWorkerSupervisorAfterDbServiceReady })'), 'server 必须在 DB service ready 后启动后台 worker')

assertRoleBlockContainsOnly('ingest-worker', [
  'api-key-record-cleanup-retry',
  'account-record-cleanup-retry',
  'audit-hot-retention-cleanup',
  'data-retention-cleanup',
  'runtime-log-index-maintenance'
])
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
assertRolePostgresBlockContains('stats-worker', 'table-storage-monitor', 'PG 高性能 stats-worker 必须注册 table-storage-monitor，避免生产表监控无采样数据')
assertRoleBlockContainsOnly('ops-worker', [
  'proxy-latency-refresh',
  'account-health-check',
  'cooldown-account-retest',
  'account-api-key-cooldown-retest',
  'openai-oauth-access-token-refresh',
  'api-key-availability-schedule-status-sync',
  'account-availability-schedule-status-sync',
  'resource-authorization-expiry-sweep',
  'expired-deleted-account-cleanup'
])
assert(backgroundJobsSource.includes('const localProcessEventLoopSample = buildProcessEventLoopSample()'), 'system-metrics-sample 本地事件循环样本必须使用当前 workerRole')

assert(workerSource.includes('if (isIngestWorker()) {'), 'worker.ts 必须把 append-only 写入队列隔离到 ingest-worker')
assert(workerSource.includes('} else if (isOpsWorker()) {'), 'ops-worker 必须启动账号测试和轻量运维本地队列')
assert(workerSource.includes('startRuntimeLogFileImport()'), 'ingest-worker 应启动运行日志文件导入')
assert(workerSource.includes('startAccountTestTaskQueue()'), 'ops-worker 应启动手动账号测试队列')
assert(workerSource.includes('getAccountApiKeyCooldownRetestQueueSnapshot') && workerSource.includes('accountApiKeyCooldownRetestQueue'), 'ops-worker runtime snapshot 必须暴露 Key 级冷却复测队列')
assert(!workerSource.includes('isProbeWorkerMessage'), 'worker.ts 不应保留 probe-worker 消息过滤')
assert(!workerSource.includes('isMaintenanceWorkerMessage'), 'worker.ts 不应保留 maintenance-worker 消息过滤')
assert(workerSource.includes('isOpsWorkerMessage'), 'worker.ts 必须禁止非 ops-worker 消费账号测试消息')
assert(workerSource.includes("message.type === 'background_worker_process_event_loop_request'"), 'worker 必须响应 server 发起的事件循环采样请求')

assert(statsRoutesSource.includes("job.name === 'account-api-key-cooldown-retest'") && statsRoutesSource.includes('opsWorkerSnapshot.accountApiKeyCooldownRetestQueue'), '系统指标后台任务表必须把 Key 级冷却复测队列挂到 ops-worker 任务行')
assert(statsRoutesSource.includes("retryQueueBackgroundJobRow('manual-account-test-queue'") && statsRoutesSource.includes('opsWorkerSnapshot?.manualAccountTestQueue'), '系统指标后台任务表必须展示手动账号测试本地队列')
assert(statsRoutesSource.includes("'account-quality-failure-precheck-queue'") && statsRoutesSource.includes('accountQualityFailurePrecheckSnapshot'), '系统指标后台任务表必须展示账号质量失败预检队列')
assert(statsRoutesSource.includes("localQueueBackgroundJobRow('record-maintenance-ingest-queue'") && statsRoutesSource.includes('ingestWorkerSnapshot?.recordMaintenanceQueue'), '系统指标后台任务表必须展示 ingest-worker 数据维护本地队列')
assert(statsRoutesSource.includes("localQueueBackgroundJobRow('record-maintenance-stats-queue'") && statsRoutesSource.includes('statsWorkerSnapshot?.recordMaintenanceQueue'), '系统指标后台任务表必须展示 stats-worker 数据维护本地队列')
assert(dbServiceIpcSource.includes('requestOpsWorkerSnapshot'), 'DB service runtime snapshot 必须请求 ops-worker 快照')
assert(dbServiceIpcSource.includes('accountApiKeyCooldownRetestQueue: opsWorkerSnapshot.accountApiKeyCooldownRetestQueue'), 'DB service runtime snapshot 必须转发 ops-worker 的 Key 级冷却复测队列')
assert(dbServiceIpcSource.includes('recordMaintenanceQueue: { ...ingestWorkerSnapshot.recordMaintenanceQueue }'), 'DB service runtime snapshot 必须转发 ingest-worker 数据维护本地队列')
assert(dbServiceIpcSource.includes('recordMaintenanceQueue: { ...statsWorkerSnapshot.recordMaintenanceQueue }'), 'DB service runtime snapshot 必须转发 stats-worker 数据维护本地队列')
assert(frontendBackgroundJobsSource.includes('const queue = row.retryQueue'), '前端后台任务表必须展示任意任务行携带的 retryQueue')
assert(frontendBackgroundJobsSource.includes('const queue = row.localQueue'), '前端后台任务表必须展示任意任务行携带的 localQueue')

assert(processMonitorSource.includes("'ops-worker'"), '事件循环采样必须识别 ops-worker')
assert(!processMonitorSource.includes("'probe-worker'"), '事件循环采样不应保留 probe-worker')
assert(backgroundIpcWorkerRolesSource.includes("return ['ingest-worker', 'stats-worker', 'ops-worker']"), '后台进程事件循环采样必须只覆盖三类 worker')
for (const role of ['server', 'ingest-worker', 'stats-worker', 'ops-worker', 'db-service']) {
  assert(systemMetricsSource.includes(`'${role}'`), `系统指标角色清单必须包含 ${role}`)
}
for (const role of retiredRoles) {
  assert(!systemMetricsSource.includes(`'${role}'`), `系统指标角色清单不应包含 ${role}`)
}

for (const job of backgroundWorkerRegistry) {
  if (job.category !== 'scheduled') continue
  assert.notEqual(job.defaultRole, 'metrics-worker', `${job.jobName} 不应默认挂到 metrics-worker`)
  assert.notEqual(job.defaultRole, 'snapshot-worker', `${job.jobName} 不应默认挂到 snapshot-worker`)
  assert.notEqual(job.defaultRole, 'probe-worker', `${job.jobName} 不应默认挂到 probe-worker`)
  assert.notEqual(job.defaultRole, 'maintenance-worker', `${job.jobName} 不应默认挂到 maintenance-worker`)
}

console.log('worker 角色回归通过：常驻后台 worker 收敛为 ingest-worker、stats-worker、ops-worker')

function readSource(path: string): string {
  return readFileSync(new URL(path, import.meta.url), 'utf8').replace(/\r\n/g, '\n')
}

function assertRoleBlockContainsOnly(role: string, jobNames: string[]): void {
  const block = roleCaseBlock(role)
  for (const jobName of jobNames) {
    assert(block.includes(`backgroundScheduledJobName('${jobName}')`), `${role} 必须注册 ${jobName}`)
  }
  const scheduledNames = [...new Set([...block.matchAll(/backgroundScheduledJobName\('([^']+)'\)/g)].map((match) => match[1]))]
  assert.deepEqual([...scheduledNames].sort(), [...jobNames].sort(), `${role} 注册任务必须和当前角色归属一致`)
  for (const jobName of jobNames) {
    assert.equal(registryByName.get(jobName)?.defaultRole, role, `${jobName} registry defaultRole 必须和 ${role} 实际挂载一致`)
  }
}

function assertRolePostgresBlockContains(role: string, jobName: string, message: string): void {
  const block = roleCaseBlock(role)
  const marker = 'if (isPostgresHighPerformanceMode()) {'
  const start = block.indexOf(marker)
  assert(start >= 0, `${role} 必须包含 PostgreSQL 高性能分支`)
  const end = block.indexOf('\n      }\n', start)
  assert(end > start, `${role} PostgreSQL 高性能分支必须有明确结束位置`)
  const postgresBlock = block.slice(start, end)
  assert(postgresBlock.includes(`backgroundScheduledJobName('${jobName}')`), message)
}

function roleCaseBlock(role: string): string {
  const marker = `case '${role}':`
  const start = backgroundJobsSource.indexOf(marker)
  assert(start >= 0, `background-jobs 必须包含 ${marker}`)
  const rest = backgroundJobsSource.slice(start + marker.length)
  const nextCase = rest.search(/\n\s*case\s+'[^']+':|\n\s*default:/)
  return nextCase >= 0 ? rest.slice(0, nextCase) : rest
}

function roleCaseExists(role: string): boolean {
  return backgroundJobsSource.includes(`case '${role}':`)
}
