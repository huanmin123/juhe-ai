import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-operation-log-queue-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  operationLogQueue,
  backgroundIpc
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js'),
  import('../../modules/background/background-ipc.js')
])

try {
  runtimeConfig.processRole = 'worker'
  operationLogQueue.enqueueOperationLogsLocal([buildOperationLog('worker_local')])
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().queueLength, 1, 'worker 角色应进入本地操作日志队列')
  operationLogQueue.flushAllOperationLogQueue()
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().queueLength, 0, 'worker flush 后队列应清空')
  assert.equal(operationLogCount(), 1, 'worker flush 应把操作日志写入记录库')

  runtimeConfig.processRole = 'server'
  const pendingBefore = backgroundIpc.getBackgroundWorkerState().pendingMessageCount
  operationLogQueue.enqueueOperationLog(buildOperationLog('server_ipc'))
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().queueLength, 0, 'server 角色不能进入本地操作日志队列')
  assert.equal(operationLogCount(), 1, 'server 角色不能同步写入记录库')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, pendingBefore + 1, 'server 角色应把操作日志投递到 worker IPC 队列')

  runtimeConfig.processRole = 'db-service'
  const droppedBefore = operationLogQueue.getOperationLogQueueRuntime().droppedCount
  operationLogQueue.enqueueOperationLog(buildOperationLog('db_service_parent_ipc_missing'))
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().queueLength, 0, 'db-service 角色不能进入本地操作日志队列')
  assert.equal(operationLogCount(), 1, 'db-service 角色不能同步写入记录库')
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().droppedCount, droppedBefore + 1, '无父进程 IPC 的 db-service 测试态应记录投递失败计数')

  console.log('操作日志队列回归通过：记录库写入只在 worker 消费端落库，server/db-service 不本地同步写入')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function buildOperationLog(action: string) {
  return {
    actorSystemAccountId: 'sys_admin',
    actorRole: 'admin' as const,
    module: 'regression',
    action,
    operationKey: `regression.${action}`,
    resourceType: 'regression',
    resourceId: action,
    resourceName: action,
    summary: `操作日志队列回归：${action}`,
    createdAt: new Date().toISOString()
  }
}

function operationLogCount(): number {
  const row = databaseModule.getRecordDatabase()
    .prepare('SELECT COUNT(*) AS total FROM operation_logs')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}
