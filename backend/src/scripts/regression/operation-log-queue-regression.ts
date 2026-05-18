import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { OperationLogInput } from '../../storage/repositories.js'

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
  backgroundIpc,
  repositories
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js'),
  import('../../modules/background/background-ipc.js'),
  import('../../storage/repositories.js')
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

  runtimeConfig.processRole = 'worker'
  const countBeforeBatch = {
    logs: operationLogCount(),
    targets: operationLogTargetCount(),
    viewers: operationLogViewerCount()
  }
  const prepareCounts = { logs: 0, targets: 0, viewers: 0 }
  const database = databaseModule.getRecordDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  database.prepare = ((sql: string) => {
    if (/^\s*INSERT\s+INTO\s+operation_logs\b/i.test(sql)) {
      prepareCounts.logs += 1
    } else if (/^\s*INSERT\s+INTO\s+operation_log_targets\b/i.test(sql)) {
      prepareCounts.targets += 1
    } else if (/^\s*INSERT\s+OR\s+IGNORE\s+INTO\s+operation_log_viewers\b/i.test(sql)) {
      prepareCounts.viewers += 1
    }
    return originalPrepare(sql)
  }) as typeof database.prepare

  try {
    operationLogQueue.enqueueOperationLogsLocal(Array.from({ length: 5 }, (_, index) => buildRichOperationLog(index)))
    operationLogQueue.flushAllOperationLogQueue()
  } finally {
    database.prepare = originalPrepare
  }

  assert.deepEqual(prepareCounts, { logs: 1, targets: 1, viewers: 1 }, '批量操作日志落库应复用三类 INSERT statement，避免按条重复 prepare')
  assert.equal(operationLogCount() - countBeforeBatch.logs, 5, '批量 flush 后应写入所有操作日志')
  assert.equal(operationLogTargetCount() - countBeforeBatch.targets, 15, '批量操作日志应完整写入显式 target 与默认 primary target')
  assert.equal(operationLogViewerCount() - countBeforeBatch.viewers, 15, '批量操作日志应完整写入显式 viewer 与默认 actor/scope viewer')
  const richDetail = repositories.getOperationLogDetail('oplog_batch_prepare_3')
  assert(richDetail, '批量写入的操作日志详情应可读取')
  assert.equal(richDetail.targets.length, 3, '操作日志详情应保留全部 target')
  assert.equal(richDetail.viewers.length, 3, '操作日志详情应保留全部 viewer')
  assert.equal(richDetail.changes[0]?.field, 'status', '操作日志详情应保留 changes payload')
  assert.equal(richDetail.metadata.batchIndex, 3, '操作日志详情应保留 metadata payload')

  console.log('操作日志队列回归通过：写入边界正确，批量落库复用 prepared statements')
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

function buildRichOperationLog(index: number): OperationLogInput {
  return {
    id: `oplog_batch_prepare_${index}`,
    actorSystemAccountId: `sys_actor_${index}`,
    actorRole: 'admin',
    operationScopeSystemAccountId: `sys_scope_${index}`,
    module: 'regression',
    action: `batch_prepare_${index}`,
    operationKey: 'regression.batch_prepare',
    resourceType: 'account',
    resourceId: `account_${index}`,
    resourceName: `批量账号 ${index}`,
    summary: `操作日志批量 prepare 回归：${index}`,
    changes: [{
      field: 'status',
      label: '状态',
      before: 'disabled',
      after: 'active'
    }],
    metadata: {
      batchIndex: index,
      source: 'operation-log-queue-regression'
    },
    method: 'POST',
    path: `/regression/operation-logs/${index}`,
    statusCode: 200,
    clientIp: '127.0.0.1',
    userAgent: 'operation-log-regression',
    targets: [{
      targetType: 'system_account',
      targetId: `sys_scope_${index}`,
      targetName: `归属用户 ${index}`,
      targetOwnerSystemAccountId: `sys_scope_${index}`,
      relation: 'owner'
    }, {
      targetType: 'group',
      targetId: `group_${index}`,
      targetName: `批量分组 ${index}`,
      targetOwnerSystemAccountId: `sys_scope_${index}`,
      relation: 'bound_resource'
    }],
    viewers: [{
      systemAccountId: `sys_extra_viewer_${index}`,
      visibilityReason: 'global_affected',
      detailLevel: 'summary'
    }],
    createdAt: new Date(Date.UTC(2026, 0, 1, 0, 0, index)).toISOString()
  }
}

function operationLogCount(): number {
  const row = databaseModule.getRecordDatabase()
    .prepare('SELECT COUNT(*) AS total FROM operation_logs')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function operationLogTargetCount(): number {
  const row = databaseModule.getRecordDatabase()
    .prepare('SELECT COUNT(*) AS total FROM operation_log_targets')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function operationLogViewerCount(): number {
  const row = databaseModule.getRecordDatabase()
    .prepare('SELECT COUNT(*) AS total FROM operation_log_viewers')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}
