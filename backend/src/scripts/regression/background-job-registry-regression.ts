import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import {
  backgroundScheduledJobs,
  backgroundWorkerRegistry,
  type BackgroundJobRegistryEntry
} from '../../modules/background/background-job-registry.js'

const backgroundJobsSource = readSource('../../modules/background/background-jobs.ts')
const workerSource = readSource('../../worker.ts')
const backgroundIpcSource = readSource('../../modules/background/background-ipc.ts')
const backgroundIpcTypesSource = readSource('../../modules/background/background-ipc.types.ts')
const backgroundIpcContractSource = `${backgroundIpcSource}\n${backgroundIpcTypesSource}`
const recordMaintenanceSource = readSource('../../modules/record-maintenance/record-maintenance-queue.service.ts')
const conditionalRuntimeModeDuplicateScheduledJobs = new Set([
  'system-metrics-sample',
  'usage-stats-aggregation',
  'client-ip-stats-aggregation',
  'usage-rank-snapshots-refresh',
  'system-metrics-trend-windows-refresh',
  'usage-overview-windows-refresh',
  'usage-scope-range-windows-refresh',
  'authorization-usage-range-windows-refresh',
  'group-account-stats-refresh',
  'account-quality-refresh',
  'usage-stats-consistency-check',
  'table-storage-monitor'
])

const registryByName = new Map<string, BackgroundJobRegistryEntry>(backgroundWorkerRegistry.map((job) => [job.jobName, job]))
const scheduledRegistryNames = new Set<string>(backgroundScheduledJobs.map((job) => job.jobName))

assertNoDuplicateRegistryNames()
assertRegistryEntriesHaveRequiredFields()
assertScheduledJobsAreRegisteredAndUsed()
assertTopologySingleOwnerJobsAreEnforced()
assertWorkerIpcMessagesAreRegistered()
assertBackgroundIpcMessagesAreRegistered()
assertWorkerEntrypointsAreRegistered()
assertRecordMaintenanceJobTypesAreRegistered()

console.log('后台 job registry 回归通过：定时任务、worker IPC、内部队列和数据维护子任务均已登记，新增后台入口漏登记会失败')

function assertNoDuplicateRegistryNames(): void {
  assert.equal(registryByName.size, backgroundWorkerRegistry.length, '后台 job registry 不允许重复 jobName')
}

function assertTopologySingleOwnerJobsAreEnforced(): void {
  assert.match(
    backgroundJobsSource,
    /runtimeConfig\.cacheDriver === 'redis' && runtimeConfig\.workerReplicaIndex === 0[\s\S]*?backgroundScheduledJobName\('usage-record-first-page-prewarm'\)/,
    'usage-record-first-page-prewarm 必须只由 replica 0 注册，避免 performance usage-worker 多副本重复预热'
  )
}

function assertRegistryEntriesHaveRequiredFields(): void {
  for (const job of backgroundWorkerRegistry) {
    assertValidEntry(job)
  }
}

function assertValidEntry(job: BackgroundJobRegistryEntry): void {
  assert(job.jobName.trim(), 'registry jobName 不能为空')
  assert(job.category.trim(), `${job.jobName} category 不能为空`)
  assert(job.kind.trim(), `${job.jobName} kind 不能为空`)
  assert(job.lifecycle.trim(), `${job.jobName} lifecycle 不能为空`)
  assert(job.defaultRole.trim(), `${job.jobName} defaultRole 不能为空`)
  assert(Array.isArray(job.writes), `${job.jobName} writes 必须是数组`)
}

function assertScheduledJobsAreRegisteredAndUsed(): void {
  assert(!/scheduler\.schedule\(\{\s*name:\s*['"]/.test(backgroundJobsSource), 'background-jobs.ts 禁止直接写 schedule name 字符串，必须使用 backgroundScheduledJobName(...)')

  const scheduledNames = collectMatches(backgroundJobsSource, /scheduler\.schedule\(\{\s*name:\s*backgroundScheduledJobName\('([^']+)'\)/g)
  const scheduleCallCount = countMatches(backgroundJobsSource, /scheduler\.schedule\(\{/g)
  assert(scheduledNames.length > 0, 'background-jobs.ts 应至少注册一个后台定时任务')
  assert.equal(scheduledNames.length, scheduleCallCount, 'background-jobs.ts 每个 scheduler.schedule 都必须用 backgroundScheduledJobName(...)')
  const duplicateScheduledNames = scheduledNames.filter((name, index) => scheduledNames.indexOf(name) !== index)
  const unexpectedDuplicateScheduledNames = duplicateScheduledNames.filter((name) => !conditionalRuntimeModeDuplicateScheduledJobs.has(name))
  assert.equal(
    unexpectedDuplicateScheduledNames.length,
    0,
    `background-jobs.ts 不应重复注册同名定时任务，除允许的 runtime-mode 分支外仍重复：${[...new Set(unexpectedDuplicateScheduledNames)].join(', ')}`
  )

  for (const name of scheduledNames) {
    assert(scheduledRegistryNames.has(name), `定时任务 ${name} 未登记到 backgroundScheduledJobs`)
  }
  for (const name of scheduledRegistryNames) {
    assert(scheduledNames.includes(name), `registry 中的定时任务 ${name} 未在 background-jobs.ts 注册`)
  }
}

function assertWorkerIpcMessagesAreRegistered(): void {
  const workerCases = collectMatches(workerSource, /case '([^']+)'/g)
    .filter((name) => name.startsWith('background_worker_'))

  assert(workerCases.length > 0, 'worker.ts 应包含 worker IPC 消息分支')
  for (const name of workerCases) {
    const entry = registryByName.get(name)
    assert(entry, `worker.ts IPC 消息 ${name} 未登记到 registry`)
    assert(entry.category === 'ipc-queue' || entry.category === 'control-ipc', `worker.ts IPC 消息 ${name} registry category 应为 ipc-queue/control-ipc`)
  }
}

function assertBackgroundIpcMessagesAreRegistered(): void {
  const ipcNames = collectMatches(backgroundIpcContractSource, /type: '([^']+)'/g)
    .filter(isBackgroundIpcRegistryName)

  assert(ipcNames.length > 0, 'background-ipc.ts / background-ipc.types.ts 应包含 IPC 消息类型')
  for (const name of new Set(ipcNames)) {
    const entry = registryByName.get(name)
    assert(entry, `background-ipc 消息 ${name} 未登记到 registry`)
    assert(entry.category === 'ipc-queue' || entry.category === 'control-ipc', `background-ipc 消息 ${name} registry category 应为 ipc-queue/control-ipc`)
  }
}

function assertWorkerEntrypointsAreRegistered(): void {
  assertRegisteredWhenSourceIncludes(workerSource, 'startRuntimeLogFileImport()', 'runtime-log-file-import')
  assertRegisteredWhenSourceIncludes(workerSource, 'startAccountTestTaskQueue()', 'manual-account-test-queue')
  assertRegisteredWhenSourceIncludes(workerSource, 'getCooldownAccountRetestQueueSnapshot()', 'cooldown-account-retest-queue')
  assertRegisteredWhenSourceIncludes(workerSource, 'getAccountQualityFailurePrecheckQueueSnapshot()', 'account-quality-failure-precheck-queue')
}

function assertRecordMaintenanceJobTypesAreRegistered(): void {
  const knownJobTypes = [
    'api_key_related_cleanup',
    'account_related_cleanup',
    'usage_records_cleanup',
    'non_business_data_cleanup',
    'audit_retained_data_cleanup',
    'account_usage_snapshot_upsert'
  ]
  for (const jobType of knownJobTypes) {
    assert(recordMaintenanceSource.includes(`'${jobType}'`), `数据维护任务类型 ${jobType} 应存在于 record-maintenance queue`)
    assert(registryByName.has(`record-maintenance:${jobType}`), `数据维护任务类型 ${jobType} 未登记到 registry`)
  }
}

function assertRegisteredWhenSourceIncludes(source: string, marker: string, jobName: string): void {
  if (!source.includes(marker)) {
    return
  }
  assert(registryByName.has(jobName), `${marker} 对应入口 ${jobName} 未登记到 registry`)
}

function isBackgroundIpcRegistryName(name: string): boolean {
  return name.startsWith('background_worker_')
    || name === 'server_account_runtime_clear'
    || name === 'gateway_runtime_cache_invalidate'
    || name === 'gateway_quota_snapshot_update'
}

function collectMatches(source: string, pattern: RegExp): string[] {
  const output: string[] = []
  let match: RegExpExecArray | null
  while ((match = pattern.exec(source)) !== null) {
    output.push(match[1])
  }
  return output
}

function countMatches(source: string, pattern: RegExp): number {
  let count = 0
  while (pattern.exec(source) !== null) {
    count += 1
  }
  return count
}

function readSource(path: string): string {
  return readFileSync(new URL(path, import.meta.url), 'utf8')
}
