import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

assertQueueShutdownFlushIsBounded()

runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
logger.level = 'silent'

const [
  usageRecordQueue,
  operationLogQueue,
  recordMaintenanceQueue,
  auditLogQueue,
  auditLogSettings,
  publicApiLogQueue,
  runtimeLogIndexQueue
] = await Promise.all([
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js'),
  import('../../modules/record-maintenance/record-maintenance-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/audit-logs/audit-log-settings.js'),
  import('../../modules/public-api-logs/public-api-log-queue.service.js'),
  import('../../modules/runtime-logs/runtime-log-index-queue.service.js')
])

try {
  usageRecordQueue.clearUsageRecordQueueForTest()
  for (let index = 0; index < 10_000; index += 1) {
    usageRecordQueue.enqueueUsageRecordsLocal([buildUsageRecord(index)])
  }
  const usageBeforeOverflow = usageRecordQueue.getUsageRecordQueueRuntime()
  assert.equal(usageBeforeOverflow.queueLength, 10_000, '使用记录 worker 本地队列应达到硬上限')
  usageRecordQueue.enqueueUsageRecordsLocal([buildUsageRecord(10_000)])
  const usageAfterOverflow = usageRecordQueue.getUsageRecordQueueRuntime()
  assert.equal(usageAfterOverflow.queueLength, 10_000, '使用记录 worker 本地队列满后不应继续增长')
  assert.equal(usageAfterOverflow.droppedOverflowCount, 1, '使用记录 worker 本地队列满后应记录溢出丢弃')
  usageRecordQueue.clearUsageRecordQueueForTest()

  runtimeConfig.workerRole = 'ingest-worker'
  operationLogQueue.clearOperationLogQueueForTest()
  for (let index = 0; index < 5000; index += 1) {
    operationLogQueue.enqueueOperationLogsLocal([buildOperationLog(index)])
  }
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().queueLength, 5000, '操作日志 worker 本地队列应达到硬上限')
  operationLogQueue.enqueueOperationLogsLocal([buildOperationLog(5000)])
  const operationAfterOverflow = operationLogQueue.getOperationLogQueueRuntime()
  assert.equal(operationAfterOverflow.queueLength, 5000, '操作日志 worker 本地队列满后不应继续增长')
  assert.equal(operationAfterOverflow.droppedOverflowCount, 1, '操作日志 worker 本地队列满后应记录溢出丢弃')
  operationLogQueue.clearOperationLogQueueForTest()

  runtimeConfig.workerRole = 'ingest-worker'
  recordMaintenanceQueue.clearRecordMaintenanceQueueForTest()
  for (let index = 0; index < 5000; index += 1) {
    const result = recordMaintenanceQueue.enqueueRecordMaintenanceJobWithResult(buildRecordMaintenanceJob(index))
    assert.equal(result.queued, true, `数据维护任务 ${index} 应进入 ingest-worker 本地队列`)
  }
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().queueLength, 5000, '数据维护 ingest-worker 本地队列应达到硬上限')
  const recordOverflow = recordMaintenanceQueue.enqueueRecordMaintenanceJobWithResult(buildRecordMaintenanceJob(5000))
  const recordAfterOverflow = recordMaintenanceQueue.getRecordMaintenanceQueueRuntime()
  assert.equal(recordOverflow.queued, false, '数据维护 ingest-worker 本地队列满后应返回未入队')
  assert.equal(recordOverflow.droppedReason, 'worker_local_queue_full')
  assert.equal(recordAfterOverflow.queueLength, 5000, '数据维护 ingest-worker 本地队列满后不应继续增长')
  assert.equal(recordAfterOverflow.droppedOverflowCount, 1, '数据维护 ingest-worker 本地队列满后应记录溢出丢弃')
  recordMaintenanceQueue.clearRecordMaintenanceQueueForTest()

  recordMaintenanceQueue.enqueueRecordMaintenanceJobWithResult(buildAccountUsageSnapshotJob('first'))
  recordMaintenanceQueue.enqueueRecordMaintenanceJobWithResult(buildAccountUsageSnapshotJob('second'))
  const mergedSnapshotRuntime = recordMaintenanceQueue.getRecordMaintenanceQueueRuntime()
  assert.equal(mergedSnapshotRuntime.queueLength, 1, '同账号同来源用量快照维护任务应在 worker 本地队列内合并')
  recordMaintenanceQueue.clearRecordMaintenanceQueueForTest()

  runtimeConfig.workerRole = 'ops-worker'
  const nonOwnerDroppedBefore = recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().droppedCount
  const nonOwnerResult = recordMaintenanceQueue.enqueueRecordMaintenanceJobWithResult(buildRecordMaintenanceJob('maintenance_non_owner'))
  assert.equal(nonOwnerResult.queued, false, 'ops-worker 不能本地写数据维护队列')
  assert.equal(nonOwnerResult.droppedReason, 'worker_ipc_unavailable', 'ops-worker 无父 IPC 时应按投递不可用拒绝')
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().queueLength, 0, 'ops-worker 不应产生本地数据维护积压')
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().droppedCount, nonOwnerDroppedBefore + 1, 'ops-worker 本地写保护应记录投递失败')

  runtimeConfig.workerRole = 'ingest-worker'
  auditLogQueue.clearAuditLogQueueForTest()
  const auditQueueMaxItems = auditLogSettings.readAuditLogSettings().queueMaxItems
  for (let index = 0; index < auditQueueMaxItems; index += 1) {
    auditLogQueue.enqueueAuditLogsLocal([buildAuditLog(index, true)])
  }
  assert.equal(auditLogQueue.getAuditLogQueueRuntime().queueLength, auditQueueMaxItems, '审计日志 worker 本地队列应达到硬上限')
  auditLogQueue.enqueueAuditLogsLocal([buildAuditLog(auditQueueMaxItems, true)])
  const auditAfterOverflow = auditLogQueue.getAuditLogQueueRuntime()
  assert.equal(auditAfterOverflow.queueLength, auditQueueMaxItems, '审计日志 worker 本地队列满后不应继续增长')
  assert.equal(auditAfterOverflow.droppedOverflowCount, 1, '审计日志 worker 本地队列满后应记录溢出丢弃')
  auditLogQueue.clearAuditLogQueueForTest()

  publicApiLogQueue.clearPublicApiLogQueueForTest()
  for (let index = 0; index < 5000; index += 1) {
    publicApiLogQueue.enqueuePublicApiLogsLocal([buildPublicApiLog(index)])
  }
  assert.equal(publicApiLogQueue.getPublicApiLogQueueRuntime().queueLength, 5000, '公开接口日志 worker 本地队列应达到硬上限')
  publicApiLogQueue.enqueuePublicApiLogsLocal([buildPublicApiLog(5000)])
  const publicApiAfterOverflow = publicApiLogQueue.getPublicApiLogQueueRuntime()
  assert.equal(publicApiAfterOverflow.queueLength, 5000, '公开接口日志 worker 本地队列满后不应继续增长')
  assert.equal(publicApiAfterOverflow.droppedCount, 1, '公开接口日志 worker 本地队列满后应记录丢弃')
  publicApiLogQueue.clearPublicApiLogQueueForTest()

  runtimeLogIndexQueue.clearRuntimeLogIndexQueueForTest()
  for (let index = 0; index < 5000; index += 1) {
    runtimeLogIndexQueue.enqueueRuntimeLogLineLocal(runtimeLogLine(index), { sourceKey: `worker-local-rtlog-${index}` })
  }
  const sampledRuntimeLog = runtimeLogIndexQueue.getRuntimeLogIndexRuntime()
  assert.equal(sampledRuntimeLog.queueLength, 4000, '低优先级运行日志索引队列应在高水位开始采样保护')
  assert.equal(sampledRuntimeLog.droppedSampledCount, 1000, '低优先级运行日志索引高水位后应记录采样丢弃')
  runtimeLogIndexQueue.clearRuntimeLogIndexQueueForTest()
  for (let index = 0; index < 5000; index += 1) {
    runtimeLogIndexQueue.enqueueRuntimeLogLineLocal(runtimeLogLine(index, 40), { sourceKey: `worker-local-rtlog-warn-${index}` })
  }
  assert.equal(runtimeLogIndexQueue.getRuntimeLogIndexRuntime().queueLength, 5000, '高优先级运行日志索引 worker 本地队列应达到硬上限')
  runtimeLogIndexQueue.enqueueRuntimeLogLineLocal(runtimeLogLine(5000, 40), { sourceKey: 'worker-local-rtlog-overflow' })
  const runtimeLogAfterOverflow = runtimeLogIndexQueue.getRuntimeLogIndexRuntime()
  assert.equal(runtimeLogAfterOverflow.queueLength, 5000, '高优先级运行日志索引 worker 本地队列满后不应继续增长')
  assert.equal(runtimeLogAfterOverflow.droppedOverflowCount, 1, '高优先级运行日志索引 worker 本地队列满后应记录溢出丢弃')
  runtimeLogIndexQueue.clearRuntimeLogIndexQueueForTest()

  console.log('worker 本地队列回归通过：使用记录、操作日志、ingest 数据维护、审计、公开接口日志和运行日志索引队列均有硬上限，非 owner worker 不能本地维护写库')
} finally {
  usageRecordQueue.clearUsageRecordQueueForTest()
  operationLogQueue.clearOperationLogQueueForTest()
  recordMaintenanceQueue.clearRecordMaintenanceQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  publicApiLogQueue.clearPublicApiLogQueueForTest()
  runtimeLogIndexQueue.clearRuntimeLogIndexQueueForTest()
}

function buildUsageRecord(index: number) {
  return {
    traceId: `trace-worker-local-usage-${index}`,
    trafficSource: 'gateway' as const,
    systemAccountId: 'sys_worker_queue',
    endpoint: 'POST /v1/responses',
    providerCode: 'gpt',
    success: true,
    stream: false,
    statusCode: 200,
    durationMs: 1,
    inputTokens: 1,
    outputTokens: 1,
    costUsd: 0,
    createdAt: '2000-01-01T00:00:00.000Z'
  }
}

function buildOperationLog(index: number) {
  return {
    actorSystemAccountId: 'sys_worker_queue',
    actorRole: 'admin' as const,
    module: 'regression',
    action: 'worker_local_queue_limit',
    operationKey: `regression.worker_local_queue_limit.${index}`,
    resourceType: 'operation_log',
    resourceId: `resource_${index}`,
    summary: 'worker 本地队列上限回归'
  }
}

function buildRecordMaintenanceJob(index: number | string) {
  return {
    type: 'usage_records_cleanup' as const,
    id: `recmaint_worker_local_${index}`,
    cutoffAt: '2000-01-01T00:00:00.000Z',
    batchSize: 100,
    maxBatches: 1
  }
}

function buildAccountUsageSnapshotJob(marker: string) {
  return {
    type: 'account_usage_snapshot_upsert' as const,
    id: `recmaint_worker_snapshot_${marker}`,
    accountId: 'acct_worker_snapshot',
    kind: 'openai_codex' as const,
    source: 'gateway',
    snapshot: {
      codex_usage_updated_at: '2000-01-01T00:00:00.000Z',
      marker
    },
    updatedAt: '2000-01-01T00:00:00.000Z'
  }
}

function buildAuditLog(index: number, success: boolean) {
  return {
    traceId: `trace-worker-local-audit-${index}`,
    method: 'POST',
    path: '/v1/responses',
    auditOutcome: success ? 'success' as const : 'gateway_failed' as const,
    success,
    finalStatusCode: success ? 200 : 503,
    errorPhase: success ? undefined : 'gateway',
    errorCode: success ? undefined : 'worker_local_queue_limit',
    errorMessage: success ? undefined : 'worker 本地队列上限回归',
    sampleBucket: 0,
    sampleReason: 'regression',
    captureStatus: 'complete' as const,
    startedAt: '2000-01-01T00:00:00.000Z',
    endedAt: '2000-01-01T00:00:00.000Z',
    attempts: [],
    payloads: []
  }
}

function buildPublicApiLog(index: number) {
  return {
    traceId: `trace-worker-local-public-api-${index}`,
    method: 'GET',
    path: '/__aipublic__/group/list',
    statusCode: 200,
    success: true,
    durationMs: 1,
    requestData: {},
    responseData: {},
    startedAt: '2000-01-01T00:00:00.000Z',
    endedAt: '2000-01-01T00:00:00.000Z',
    createdAt: '2000-01-01T00:00:00.000Z'
  }
}

function runtimeLogLine(index: number, level = 30): string {
  return JSON.stringify({
    time: '2000-01-01T00:00:00.000Z',
    level,
    event: 'worker_local_queue_limit',
    msg: `worker 本地队列上限回归 ${index}`
  })
}

function assertQueueShutdownFlushIsBounded(): void {
  const queueFiles = [
    '../../modules/gateway/usage/record-queue.service.ts',
    '../../modules/operation-logs/operation-log-queue.service.ts',
    '../../modules/record-maintenance/record-maintenance-queue.service.ts',
    '../../modules/public-api-logs/public-api-log-queue.service.ts',
    '../../modules/runtime-logs/runtime-log-index-queue.service.ts',
    '../../modules/audit-logs/audit-log-queue.service.ts'
  ]

  for (const file of queueFiles) {
    const source = readFileSync(new URL(file, import.meta.url), 'utf8')
    assert(!/process\.once\(\s*['"]exit['"]/.test(source), `${file} 不应注册 exit 同步 flush 钩子`)
    assert(source.includes('QueueForShutdown'), `${file} 应提供退出路径专用的有限 flush`)
    assert(source.includes('ShutdownFlushMaxBatches = 1'), `${file} 退出路径 flush 必须按批次数硬限制`)
    assert(source.includes('maxBatches:'), `${file} 退出路径 flush 必须传入 maxBatches`)
  }

  const workerSource = readFileSync(new URL('../../worker.ts', import.meta.url), 'utf8')
  assert(workerSource.includes('installWorkerSignalShutdownHooks()'), 'worker 应由统一信号钩子协调各本地队列退出 flush')
  assert(workerSource.includes('flushWorkerQueuesForShutdown'), 'worker 信号退出应统一有限 flush 全部本地队列')
  assert(workerSource.includes('await flushRecordMaintenanceQueueForShutdown()'), 'worker 信号退出应等待数据维护异步有限 flush 完成')
  assert(workerSource.includes('await flushAuditLogQueueForShutdown()'), 'worker 信号退出应等待审计日志异步有限 flush 完成')
}
