import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-operation-log-db-service-ipc-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'operation-log-db-service-ipc-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const originalSend = process.send
const [{ enqueueOperationLog, getOperationLogQueueRuntime }, databaseModule] = await Promise.all([
  import('../../modules/operation-logs/operation-log-queue.service.js'),
  import('../../storage/database.js')
])

try {
  process.send = (() => {
    throw new Error('模拟父进程 IPC 已关闭')
  }) as NodeJS.Process['send']

  const before = getOperationLogQueueRuntime().droppedCount
  assert.doesNotThrow(() => enqueueOperationLog({
    actorSystemAccountId: 'sys_admin',
    actorRole: 'admin',
    module: 'regression',
    action: 'db_service_ipc_closed',
    operationKey: 'regression.operation_log_db_service_ipc',
    resourceType: 'operation_log',
    summary: 'DB service 操作日志 IPC 断开回归'
  }), 'DB service 操作日志投递 IPC 断开时不应抛出异常')
  const after = getOperationLogQueueRuntime().droppedCount
  assert.equal(after, before + 1, 'IPC 投递失败应累计 droppedCount')

  process.send = ((message: unknown, callback?: (error: Error | null) => void) => {
    void message
    callback?.(new Error('模拟父进程 IPC 异步失败'))
    return true
  }) as NodeJS.Process['send']
  const asyncBefore = getOperationLogQueueRuntime().droppedCount
  enqueueOperationLog({
    actorSystemAccountId: 'sys_admin',
    actorRole: 'admin',
    module: 'regression',
    action: 'db_service_ipc_async_failed',
    operationKey: 'regression.operation_log_db_service_ipc_async_failed',
    resourceType: 'operation_log',
    summary: 'DB service 操作日志 IPC 异步失败回归'
  })
  assert.equal(getOperationLogQueueRuntime().droppedCount, asyncBefore + 1, 'IPC 异步投递失败也应累计 droppedCount')

  console.log('操作日志 DB service IPC 回归通过：父进程通道异常时不会打崩 DB service，并能记录异步发送失败')
} finally {
  process.send = originalSend
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
