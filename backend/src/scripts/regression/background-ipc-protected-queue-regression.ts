import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-background-ipc-protected-queue-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [backgroundIpc, recordMaintenanceQueue] = await Promise.all([
  import('../../modules/background/background-ipc.js'),
  import('../../modules/record-maintenance/record-maintenance-queue.service.js')
])

try {
  for (let index = 0; index < 5000; index += 1) {
    const result = recordMaintenanceQueue.enqueueRecordMaintenanceJobWithResult(buildUsageRecordsCleanupJob(index))
    assert.equal(result.queued, true, `记录库维护任务 ${index} 应被 server IPC 队列保留`)
  }
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5000, '应填满 regular IPC 队列')

  const protectedOverflow = recordMaintenanceQueue.enqueueRecordMaintenanceJobWithResult(buildUsageRecordsCleanupJob(5000))
  assert.equal(protectedOverflow.queued, true, '队列已满时记录库维护任务仍应被保留')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5001, '不可丢弃维护任务不应被溢出兜底移除')

  const runtimeLogAccepted = backgroundIpc.sendRuntimeLogLineToWorker('{"level":"info","event":"runtime_log_after_protected_queue_full"}')
  assert.equal(runtimeLogAccepted, false, '队列已由不可丢弃任务占满时，低优先级运行日志应返回投递失败')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, 5001, '低优先级消息被拒绝时不应挤掉维护任务')

  console.log('后台 IPC 保护队列回归通过：记录库维护任务不会被 regular 队列溢出静默丢弃')
} finally {
  rmSync(tempRoot, { recursive: true, force: true })
}

function buildUsageRecordsCleanupJob(index: number) {
  return {
    type: 'usage_records_cleanup' as const,
    id: `recmaint_ipc_protected_${index}`,
    cutoffAt: '2000-01-01T00:00:00.000Z',
    batchSize: 100,
    maxBatches: 1
  }
}
