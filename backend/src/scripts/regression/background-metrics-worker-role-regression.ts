import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { backgroundWorkerRegistry } from '../../modules/background/background-job-registry.js'

const supervisorSource = readSource('../../modules/background/background-worker-supervisor.ts')
const backgroundJobsSource = readSource('../../modules/background/background-jobs.ts')
const workerSource = readSource('../../worker.ts')
const runtimeSource = readSource('../../config/runtime.ts')
const processMonitorSource = readSource('../../shared/process-event-loop-monitor.ts')
const systemMetricsSource = readSource('../../storage/system-metrics.repository.ts')

const registryByName = new Map(backgroundWorkerRegistry.map((job) => [job.jobName, job]))

assert(runtimeSource.includes("workerRole: workerRoleConfig('JUHE_AI_WORKER_ROLE', 'worker')"), 'runtimeConfig 必须支持 JUHE_AI_WORKER_ROLE')
assert(supervisorSource.includes("['worker', 'metrics-worker', 'ingest-worker']"), 'supervisor 必须同时声明默认 worker、metrics-worker 和 ingest-worker')
assert(supervisorSource.includes("JUHE_AI_WORKER_ROLE: role"), 'supervisor fork 子进程时必须传入 worker role')
assert(supervisorSource.includes('attachBackgroundWorkerProcess(child, {') && supervisorSource.includes('role,'), 'supervisor attach worker IPC 时必须传入 role')

assert(backgroundJobsSource.includes("runtimeConfig.workerRole === 'metrics-worker'"), 'background-jobs 必须按 workerRole 过滤 metrics-worker 任务')
assert(backgroundJobsSource.includes("backgroundScheduledJobName('system-metrics-sample')"), 'metrics-worker 必须注册 system-metrics-sample')
assert(backgroundJobsSource.includes('return\n  }\n\n  scheduler.schedule({ name: backgroundScheduledJobName(\'usage-stats-aggregation\')'), 'metrics-worker 注册 system-metrics-sample 后必须直接返回，不能继续注册统计重任务')
assert(!backgroundJobsSource.includes("buildProcessEventLoopSample('worker')"), 'system-metrics-sample 已在 metrics-worker 内运行，不能把本地事件循环样本硬编码成 worker')
assert(backgroundJobsSource.includes('const localProcessEventLoopSample = buildProcessEventLoopSample()'), 'system-metrics-sample 本地事件循环样本必须使用当前 workerRole')

assert(workerSource.includes('if (isIngestWorker()) {'), 'worker.ts 必须把 append-only 写入队列隔离到 ingest-worker')
assert(workerSource.includes('} else if (isDefaultWorker()) {'), '默认 worker 和 ingest-worker 的启动入口必须分离')
assert(workerSource.includes('startRuntimeLogFileImport()'), 'ingest-worker 应启动运行日志文件导入')
assert(workerSource.includes('startAccountTestTaskQueue()'), '默认 worker 仍应启动手动账号测试队列')
assert(workerSource.includes('isIngestWorkerMessage'), 'worker.ts 必须禁止默认 worker 消费 ingest-worker 消息')
assert(workerSource.includes("message.type === 'background_worker_process_event_loop_request'"), 'worker 必须响应 server 发起的事件循环采样请求')

assert(processMonitorSource.includes("runtimeConfig.workerRole"), '事件循环采样必须使用 workerRole 区分 metrics-worker')
assert(systemMetricsSource.includes("'metrics-worker'"), '系统指标角色清单必须包含 metrics-worker')
assert(systemMetricsSource.includes("'ingest-worker'"), '系统指标角色清单必须包含 ingest-worker')

const metricsJob = registryByName.get('system-metrics-sample')
assert(metricsJob, 'system-metrics-sample 必须登记到 registry')
assert.equal(metricsJob.defaultRole, 'metrics-worker', 'system-metrics-sample 默认角色必须是 metrics-worker')
assert.equal(metricsJob.lifecycle, 'persistent', 'system-metrics-sample 必须是持久 worker 任务')

for (const job of backgroundWorkerRegistry) {
  if (job.jobName === 'system-metrics-sample' || job.category !== 'scheduled') {
    continue
  }
  assert.notEqual(job.defaultRole, 'metrics-worker', `${job.jobName} 不应默认挂到 metrics-worker`)
}

console.log('metrics-worker 角色回归通过：系统采样独立到 metrics-worker，默认 worker 不再承载采样任务，metrics-worker 不启动业务队列')

function readSource(path: string): string {
  return readFileSync(new URL(path, import.meta.url), 'utf8').replace(/\r\n/g, '\n')
}
