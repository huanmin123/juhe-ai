import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { backgroundWorkerRegistry } from '../../modules/background/background-job-registry.js'
import { workerMessageTargetRole } from '../../modules/background/background-ipc-worker-roles.js'
import type { BackgroundWorkerMessage } from '../../modules/background/background-ipc.types.js'

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
const dbServiceHandlersSource = readSource('../../modules/db-service/db-service-handlers.ts')
const frontendBackgroundJobsSource = readSource('../../../../frontend/src/views/stats/StatsBackgroundJobsCard.vue')
const frontendBackgroundQueuesSource = readSource('../../../../frontend/src/views/stats/StatsBackgroundQueuesCard.vue')
const frontendBackgroundQueuesHelperSource = readSource('../../../../frontend/src/views/stats/statsBackgroundQueues.ts')
const frontendSystemMetricsSource = readSource('../../../../frontend/src/views/stats/SystemMetricsStatsView.vue')
const systemMetricsRouteSource = statsRoutesSource.match(/statsRouter\.get\('\/system-metrics\/trend',[\s\S]*?\n\}\)\n/)?.[0]
const systemMetricsRuntimeSummaryRouteSource = statsRoutesSource.match(/statsRouter\.get\('\/system-metrics\/runtime\/summary',[\s\S]*?\n\}\)\n/)?.[0]
const systemMetricsRuntimeJobsRouteSource = statsRoutesSource.match(/statsRouter\.get\('\/system-metrics\/runtime\/jobs',[\s\S]*?\n\}\)\n/)?.[0]
const systemMetricsRuntimeQueuesRouteSource = statsRoutesSource.match(/statsRouter\.get\('\/system-metrics\/runtime\/queues',[\s\S]*?\n\}\)\n/)?.[0]

assert(systemMetricsRouteSource, '系统指标窄趋势路由必须存在')
assert.doesNotMatch(statsRoutesSource, /statsRouter\.get\('\/system-metrics',/, '无页面消费者的旧宽系统指标路由必须退场')
assert(systemMetricsRuntimeSummaryRouteSource && systemMetricsRuntimeJobsRouteSource && systemMetricsRuntimeQueuesRouteSource, '系统指标运行态分段路由必须存在')
assert.doesNotMatch(systemMetricsRouteSource, /requestServerRuntimeSnapshot|backgroundQueueRuntimeRows/, '趋势路由不得等待 worker / 队列运行态')
assert.match(systemMetricsRuntimeSummaryRouteSource, /loadSystemMetricsRuntimeSnapshot\(\)/, '运行态摘要必须独立请求专用窄 server snapshot')
assert.match(systemMetricsRuntimeJobsRouteSource, /systemMetricsRuntimeJobRows\(runtime\)/, '运行态任务必须按分页 DTO 投影')
assert.match(systemMetricsRuntimeQueuesRouteSource, /systemMetricsRuntimeQueueRows\(\[/, '运行态队列必须按分页 DTO 投影')

const registryByName = new Map<string, typeof backgroundWorkerRegistry[number]>(backgroundWorkerRegistry.map((job) => [job.jobName, job]))
const expectedSupervisedRoles = ['ingest-worker', 'stats-worker', 'ops-worker'] as const
const retiredRoles = ['metrics-worker', 'snapshot-worker', 'probe-worker', 'maintenance-worker'] as const

for (const type of [
  'background_worker_usage_records',
  'background_worker_public_api_logs',
  'background_worker_record_maintenance',
  'background_worker_dataset_write_request'
] as const) {
  assert.equal(workerMessageTargetRole({ type } as BackgroundWorkerMessage), 'ingest-worker', `${type} 必须路由到 ingest-worker`)
}
for (const type of [
  'background_worker_account_test_tasks',
  'background_worker_account_test_cancel'
] as const) {
  assert.equal(workerMessageTargetRole({ type } as BackgroundWorkerMessage), 'ops-worker', `${type} 必须路由到 ops-worker`)
}

assert(runtimeSource.includes("workerRole: workerRoleConfig('JUHE_AI_WORKER_ROLE', 'worker')"), 'runtimeConfig 必须支持 JUHE_AI_WORKER_ROLE')
for (const role of expectedSupervisedRoles) {
  assert(supervisorSource.includes(`'${role}'`), `supervisor 必须声明 ${role}`)
}
for (const role of retiredRoles) {
  assert(!roleCaseExists(role), `background-jobs 不应保留 ${role} 独立调度分支`)
}
assert(supervisorSource.includes('JUHE_AI_WORKER_ROLE: spec.role'), 'supervisor fork 子进程时必须传入副本规格中的 worker role')
assert(supervisorSource.includes('JUHE_AI_WORKER_REPLICA_INDEX: String(spec.replicaIndex)'), 'supervisor fork 子进程时必须传入 worker replica index')
assert(supervisorSource.includes('attachBackgroundWorkerProcess(child, {') && supervisorSource.includes('role,'), 'supervisor attach worker IPC 时必须传入 role')
assert(supervisorSource.includes('startWorkerProcessesInSequence()'), 'supervisor 首次启动必须按序启动 worker，避免多个 worker 同时初始化 SQLite')
assert(serverSource.includes('startDbServiceSupervisor({') && serverSource.includes('onReady: startBackgroundWorkerSupervisorAfterDbServiceReady'), 'server 必须在 DB service ready 后启动后台 worker')

assertRoleBlockContainsOnly('ingest-worker', [
  'api-key-record-cleanup-retry',
  'account-record-cleanup-retry',
  'data-retention-cleanup'
])
assertRoleBlockContainsOnly('stats-worker', [
  'background-task-run-reconcile',
  'system-metrics-sample',
  'usage-stats-aggregation',
  'client-ip-stats-aggregation',
  'group-account-stats-refresh',
  'model-trust-observation-aggregation',
  'usage-rank-snapshots-refresh',
  'ai-performance-summary-windows-refresh',
  'system-metrics-trend-windows-refresh',
  'usage-overview-windows-refresh',
  'usage-scope-range-windows-refresh',
  'authorization-usage-range-windows-refresh',
  'account-quality-refresh',
  'usage-stats-consistency-check'
])
assertRolePostgresBlockContains('stats-worker', 'group-account-stats-refresh', 'PG 高性能 stats-worker 必须注册 group-account-stats-refresh，避免分组账户统计缓存长期不刷新')
assertRolePostgresBlockContains('stats-worker', 'account-quality-refresh', 'PG 高性能 stats-worker 必须注册 account-quality-refresh，避免账户质量分长期不刷新')
assertRolePostgresBlockContains('stats-worker', 'usage-stats-consistency-check', 'PG 高性能 stats-worker 必须注册 usage-stats-consistency-check，避免统计一致性漂移无人检测')
assertRolePostgresBlockContains('stats-worker', 'authorization-usage-range-windows-refresh', 'PG 高性能 stats-worker 必须注册授权用量范围窗口刷新')
assert.match(backgroundJobsSource, /runtimeConfig\.databaseDriver === 'postgres'[\s\S]*refreshBackgroundJobSettingsSnapshotIfNeeded\(\)[\s\S]*generation !== startGeneration[\s\S]*scheduleBackgroundJobs\(\)/, 'PG 后台定时任务必须等待系统设置快照加载，并使用 generation 防止停机后复活')
assert.match(backgroundJobsSource, /function refreshBackgroundJobSettingsSnapshotIfNeeded\(\): Promise<void>/, 'PG 后台任务系统设置快照刷新必须返回 Promise，避免启动时异步未完成就注册默认 interval')
assert(backgroundJobsSource.includes("reason: 'stats_worker_startup_refresh'"), 'PG stats-worker 首次分组统计刷新必须写全量脏标记，修复已有统计缓存缺失或旧 0 值')
assertRoleBlockContainsOnly('ops-worker', [
  'chat-retention-cleanup',
  'proxy-latency-refresh',
  'account-balance-refresh',
  'account-balance-auto-detect-recovery',
  'account-circuit-control-plane-maintenance',
  'account-list-availability-projection-maintenance',
  'account-circuit-recovery',
  'model-quality-scheduled-check',
  'model-quality-recovery',
  'model-quality-health-sync-retry',
  'account-api-key-cooldown-retest',
  'openai-oauth-access-token-refresh',
  'api-key-availability-schedule-status-sync',
  'account-availability-schedule-status-sync',
  'resource-authorization-expiry-sweep',
  'expired-deleted-account-cleanup',
  'normal-route-speed-first-recovery-probe'
])
assert(backgroundJobsSource.includes('const localProcessEventLoopSample = buildProcessEventLoopSample()'), 'system-metrics-sample 本地事件循环样本必须使用当前 workerRole')

assert(workerSource.includes('if (isIngestWorker()) {'), 'worker.ts 必须把 append-only 写入队列隔离到 ingest-worker')
assert(workerSource.includes('await stopBackgroundJobs()') && workerSource.indexOf('await stopBackgroundJobs()') < workerSource.indexOf('await stopUsageRecordRedisStreamConsumer()'), 'worker 停机必须先停止后台 producer，再排空消费队列')
assert(workerSource.includes('} else if (isOpsWorker()) {'), 'ops-worker 必须启动账号测试和轻量运维本地队列')
assert(workerSource.includes('startAccountTestTaskQueue()'), 'ops-worker 应启动手动账号测试队列')
assert(workerSource.includes('getAccountApiKeyCooldownRetestQueueSnapshot') && workerSource.includes('accountApiKeyCooldownRetestQueue'), 'ops-worker runtime snapshot 必须暴露 Key 级冷却复测队列')
assert(!workerSource.includes('isProbeWorkerMessage'), 'worker.ts 不应保留 probe-worker 消息过滤')
assert(!workerSource.includes('isMaintenanceWorkerMessage'), 'worker.ts 不应保留 maintenance-worker 消息过滤')
assert(workerSource.includes('isOpsWorkerMessage'), 'worker.ts 必须禁止非 ops-worker 消费账号测试消息')
assert(workerSource.includes("message.type === 'background_worker_process_event_loop_request'"), 'worker 必须响应 server 发起的事件循环采样请求')

assert(statsRoutesSource.includes("retryQueueBackgroundJobRow('account-api-key-cooldown-retest-queue'") && statsRoutesSource.includes('opsWorkerSnapshot?.accountApiKeyCooldownRetestQueue'), '系统指标接口必须保留 Key 级冷却复测队列运行态')
assert(statsRoutesSource.includes("retryQueueBackgroundJobRow('manual-account-test-queue'") && statsRoutesSource.includes('opsWorkerSnapshot?.manualAccountTestQueue'), '系统指标接口必须保留手动账号测试本地队列运行态')
assert(statsRoutesSource.includes("'account-quality-failure-precheck-queue'") && statsRoutesSource.includes('accountQualityFailurePrecheckSnapshot'), '系统指标接口必须保留账号质量失败预检队列运行态')
assert(dbServiceIpcSource.includes('requestOpsWorkerSnapshot'), 'DB service runtime snapshot 必须请求 ops-worker 快照')
assert(dbServiceIpcSource.includes('accountApiKeyCooldownRetestQueue: opsWorkerSnapshot.accountApiKeyCooldownRetestQueue'), 'DB service runtime snapshot 必须转发 ops-worker 的 Key 级冷却复测队列')
assert(dbServiceIpcSource.includes('normalRouteSpeedFirstRecoveryProbeQueue: opsWorkerSnapshot.normalRouteSpeedFirstRecoveryProbeQueue'), 'DB service runtime snapshot 必须转发普通路由速度优先恢复探针队列')
assert(statsRoutesSource.includes("retryQueueBackgroundJobRow(\n      'normal-route-speed-first-recovery-probe-queue'") && statsRoutesSource.includes('opsWorkerSnapshot?.normalRouteSpeedFirstRecoveryProbeQueue'), '系统指标接口必须展示普通路由速度优先恢复探针队列')
assert(dbServiceIpcSource.includes('recordMaintenanceQueue: { ...ingestWorkerSnapshot.recordMaintenanceQueue }'), 'DB service runtime snapshot 必须转发 ingest-worker 数据维护本地队列')
assert(dbServiceIpcSource.includes('recordMaintenanceQueue: { ...statsWorkerSnapshot.recordMaintenanceQueue }'), 'DB service runtime snapshot 必须转发 stats-worker 数据维护本地队列')
assert(dbServiceIpcSource.includes("import('../gateway/runtime/high-concurrency-queue.service.js')") && dbServiceIpcSource.includes('highConcurrencyQueues: highConcurrencyQueue.highConcurrencyGroupQueueSnapshot()'), 'server runtime snapshot 必须暴露高并发短队列')
assert(dbServiceHandlersSource.includes('getCodexContextStateWriterPoolRuntime()') && dbServiceIpcSource.includes('codexContextStateWriterPool: dbServiceState.lastSnapshot?.codexContextStateWriterPool'), 'DB service runtime snapshot 必须暴露 Codex 状态写入池队列')
assert(statsRoutesSource.includes('buildBackgroundQueueHealthSnapshot(runtime)') && statsRoutesSource.includes('queueHealth.workerQueues') && statsRoutesSource.includes('queueHealth.serverIpcQueues'), '系统指标接口必须复用后台队列健康快照接入 worker 本地队列和 IPC 队列')
assert(!statsRoutesSource.includes('loadMockBackgroundRuntimeSnapshot'), '系统指标接口不能在 runtime snapshot 不可用时回退 mock 运行态，避免误导运维排障')
assert(statsRoutesSource.includes('redisStreamRuntimeQueueRows()') && statsRoutesSource.includes('Redis Stream 使用记录'), '系统指标接口必须接入仍存在的高性能模式 Redis Stream 队列')
assert(statsRoutesSource.includes('dbServiceRuntimeQueueRows(runtime)') && statsRoutesSource.includes('DB service 请求队列') && statsRoutesSource.includes('DB service dataset-writer pending') && statsRoutesSource.includes('DB service Codex 状态写入池'), '系统指标接口必须接入 DB service 请求队列和写入池队列')
assert(statsRoutesSource.includes('gatewayAccountSideEffectQueueRows(runtime)') && statsRoutesSource.includes('网关账号副作用队列'), '系统指标接口必须接入网关账号副作用队列')
assert(statsRoutesSource.includes('highConcurrencyRuntimeQueueRows(runtime)') && statsRoutesSource.includes('高并发短队列'), '系统指标接口必须接入高并发短队列')
assert(frontendSystemMetricsSource.includes('api.stats.systemMetricsRuntimeJobs({ page: backgroundJobPage.value, pageSize: backgroundJobPageSize }') && frontendSystemMetricsSource.includes('api.stats.systemMetricsRuntimeQueues({ page: backgroundQueuePage.value, pageSize: backgroundQueuePageSize }'), '前端后台任务 / 队列卡片必须分段异步加载运行态')
assert(!frontendSystemMetricsSource.includes('filter(isBackgroundTaskRow)') && statsRoutesSource.includes('systemMetricsRuntimeJobRows(runtime)'), '后台任务伪行过滤必须收敛到服务端任务投影')
assert(!frontendBackgroundJobsSource.includes('队列：') && !frontendBackgroundJobsSource.includes('队列状态') && !frontendBackgroundJobsSource.includes('backgroundJobQueueSummary'), '前端后台任务表不能展示队列摘要，避免把队列误认为任务')
assert(frontendSystemMetricsSource.includes('<StatsBackgroundQueuesCard') && frontendSystemMetricsSource.includes('backgroundQueuesResult.value?.items'), '系统指标页必须把后台队列拆到独立运行态列表')
assert(frontendBackgroundQueuesHelperSource.includes('.flatMap(backgroundQueueRowsFromRuntimeRow)') && frontendBackgroundQueuesHelperSource.includes('row.retryQueue') && frontendBackgroundQueuesHelperSource.includes('row.localQueue'), '后台队列列表必须从 retryQueue 和 localQueue 独立构建')
for (const columnTitle of ['积压', '活跃', '容量 / 处理', '异常累计', '最老等待', '调度 / 写入', 'Redis Stream']) {
  assert(frontendBackgroundQueuesSource.includes(columnTitle), `后台队列列表必须包含 ${columnTitle} 列`)
}
assert.match(frontendSystemMetricsSource, /<a-col :xs="24">\s*<StatsChartCard\s+:title="`进程事件循环延迟/, '系统指标页进程事件循环延迟必须独占整行展示')
assert.match(frontendSystemMetricsSource, /<a-col :xs="24">\s*<StatsBackgroundJobsCard/, '系统指标页后台任务运行状态必须独占整行展示')
assert(!frontendSystemMetricsSource.includes(':xl="14"') && !frontendSystemMetricsSource.includes(':xl="10"'), '系统指标页不能把进程事件循环延迟和后台任务运行状态用大屏分栏挤在同一行')
const processEventLoopCardIndex = frontendSystemMetricsSource.indexOf(':title="`进程事件循环延迟')
const processMemoryCardIndex = frontendSystemMetricsSource.indexOf(':title="`进程 RSS 峰值趋势')
const backgroundJobsCardIndex = frontendSystemMetricsSource.indexOf('<StatsBackgroundJobsCard')
const backgroundQueuesCardIndex = frontendSystemMetricsSource.indexOf('<StatsBackgroundQueuesCard')
assert(processEventLoopCardIndex >= 0 && processMemoryCardIndex > processEventLoopCardIndex && backgroundJobsCardIndex > processMemoryCardIndex && backgroundQueuesCardIndex > backgroundJobsCardIndex, '系统指标页后台任务和后台队列列表必须放在系统趋势卡片之后，且队列列表跟在任务列表后面')
assert(!frontendBackgroundJobsSource.includes('展示各后台 worker 内定时任务的最近耗时、失败、跳过和关键本地队列情况。'), '后台任务运行状态不展示说明文案')
assert(!frontendBackgroundJobsSource.includes('成功 / 失败 / 跳过'), '后台任务表不能把成功、失败、跳过挤在同一列')
assert(frontendBackgroundJobsSource.includes("{ title: '成功', key: 'successCount'") && frontendBackgroundJobsSource.includes("sorter: sortBackgroundJobNumber('successCount')"), '后台任务成功次数必须单独成列并支持排序')
assert(frontendBackgroundJobsSource.includes("{ title: '累计失败（本进程）', key: 'failureCount'") && frontendBackgroundJobsSource.includes("sorter: sortBackgroundJobNumber('failureCount'), defaultSortOrder: 'descend'"), '后台任务失败次数必须明确进程内累计作用域、支持排序并默认倒序')
assert(frontendBackgroundJobsSource.includes("{ title: '跳过', key: 'skippedCount'") && frontendBackgroundJobsSource.includes("sorter: sortBackgroundJobNumber('skippedCount')"), '后台任务跳过次数必须单独成列并支持排序')
assert(frontendBackgroundJobsSource.includes("{ title: '最近开始', key: 'lastStartedAt'"), '后台任务必须展示最近开始时间')
assert(frontendBackgroundJobsSource.includes("{ title: '本进程运行', key: 'runCount'"), '后台任务必须展示本进程运行次数')

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
  const scheduledNames = collectScheduledJobNames(block)
  for (const jobName of jobNames) {
    assert(scheduledNames.includes(jobName), `${role} 必须注册 ${jobName}`)
  }
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
  const directRegistration = postgresBlock.includes(`backgroundScheduledJobName('${jobName}')`)
  const helperRegistration = jobName === 'account-quality-refresh'
    && postgresBlock.includes('scheduleAccountQualityRefreshJob()')
    && functionBlock('scheduleAccountQualityRefreshJob').includes(`backgroundScheduledJobName('${jobName}')`)
  assert(directRegistration || helperRegistration, message)
}

function roleCaseBlock(role: string): string {
  const marker = `case '${role}':`
  const start = backgroundJobsSource.indexOf(marker)
  assert(start >= 0, `background-jobs 必须包含 ${marker}`)
  const rest = backgroundJobsSource.slice(start + marker.length)
  const nextCase = rest.search(/\n\s*case\s+'[^']+':|\n\s*default:/)
  const block = nextCase >= 0 ? rest.slice(0, nextCase) : rest
  if (role === 'ingest-worker') {
    return `${block}\n${functionBlock('scheduleUsageIngestJobs')}\n${functionBlock('scheduleLogIngestJobs')}`
  }
  if (role === 'stats-worker') {
    return `${block}\n${functionBlock('scheduleAccountQualityRefreshJob')}`
  }
  return block
}

function collectScheduledJobNames(block: string): string[] {
  const names = new Set([...block.matchAll(/backgroundScheduledJobName\('([^']+)'\)/g)].map((match) => match[1]))
  return [...names]
}

function functionBlock(name: string): string {
  const marker = `function ${name}(`
  const start = backgroundJobsSource.indexOf(marker)
  assert(start >= 0, `background-jobs 必须包含 ${marker}`)
  const rest = backgroundJobsSource.slice(start)
  const nextFunction = rest.slice(marker.length).search(/\n(?:async\s+)?function\s+/)
  return nextFunction >= 0 ? rest.slice(0, marker.length + nextFunction) : rest
}

function roleCaseExists(role: string): boolean {
  return backgroundJobsSource.includes(`case '${role}':`)
}
