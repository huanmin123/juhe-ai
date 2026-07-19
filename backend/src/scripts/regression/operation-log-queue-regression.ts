import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { OperationLogInput } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-operation-log-queue-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  operationLogQueue,
  operationLogService,
  backgroundIpc,
  repositories
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/operation-logs/operation-log-queue.service.js'),
  import('../../modules/operation-logs/operation-log.service.js'),
  import('../../modules/background/background-ipc.js'),
  import('../../storage/repositories.js')
])

try {
  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'ingest-worker'
  operationLogQueue.enqueueOperationLogsLocal([buildOperationLog('worker_local')])
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().queueLength, 1, 'ingest-worker 角色应进入本地操作日志队列')
  operationLogQueue.flushAllOperationLogQueue()
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().queueLength, 0, 'worker flush 后队列应清空')
  assert.equal(operationLogCount(), 1, 'ingest-worker flush 应把操作日志写入数据集目录库')

  runtimeConfig.processRole = 'server'
  const pendingBefore = backgroundIpc.getBackgroundWorkerState().pendingMessageCount
  operationLogQueue.enqueueOperationLog(buildOperationLog('server_ipc'))
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().queueLength, 0, 'server 角色不能进入本地操作日志队列')
  assert.equal(operationLogCount(), 1, 'server 角色不能同步写入数据集目录库')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, pendingBefore + 1, 'server 角色应把操作日志投递到 worker IPC 队列')

  runtimeConfig.processRole = 'db-service'
  const droppedBefore = operationLogQueue.getOperationLogQueueRuntime().droppedCount
  operationLogQueue.enqueueOperationLog(buildOperationLog('db_service_parent_ipc_missing'))
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().queueLength, 0, 'db-service 角色不能进入本地操作日志队列')
  assert.equal(operationLogCount(), 1, 'db-service 角色不能同步写入数据集目录库')
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().droppedCount, droppedBefore + 1, '无父进程 IPC 的 db-service 测试态应记录投递失败计数')

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'ingest-worker'
  const countBeforeBatch = {
    logs: operationLogCount(),
    targets: operationLogTargetCount(),
    viewers: operationLogViewerCount()
  }
  const prepareCounts = { logs: 0, targets: 0, viewers: 0 }
  const database = databaseModule.getDatasetDatabase()
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

  const persistedSecrets = {
    wifJwt: 'eyJhbGciOiJSUzI1NiJ9.wif-payload.wif-signature',
    googleRefreshToken: '1//google-refresh-token-sensitive',
    openAIRefreshToken: 'rt_openai_refresh_token_sensitive',
    clientSecret: 'google-client-secret-sensitive',
    apiKey: 'sk-ant-api-key-sensitive',
    proxyPassword: 'proxy-password-sensitive'
  }
  const ordinaryText = 'token/key/secret/credentials 字样只是字段说明，不应做任意文本正则扫描'
  const sensitiveChanges = operationLogService.sanitizeOperationChanges([{
    field: 'credentials',
    label: '账户凭据',
    before: undefined,
    after: {
      identity_token: persistedSecrets.wifJwt,
      google_refresh_token: persistedSecrets.googleRefreshToken,
      openai_refresh_token: persistedSecrets.openAIRefreshToken
    }
  }, {
    field: 'token',
    label: 'Token',
    before: undefined,
    after: persistedSecrets.googleRefreshToken
  }, {
    field: 'key',
    label: 'Key',
    before: undefined,
    after: persistedSecrets.apiKey
  }, {
    field: 'secret',
    label: 'Secret',
    before: undefined,
    after: persistedSecrets.clientSecret
  }, {
    field: 'apiKey',
    label: 'API Key',
    before: undefined,
    after: persistedSecrets.apiKey
  }, {
    field: 'access_token',
    label: 'Access Token',
    before: undefined,
    after: persistedSecrets.wifJwt
  }, {
    field: 'refresh_token',
    label: 'Refresh Token',
    before: undefined,
    after: persistedSecrets.openAIRefreshToken
  }, {
    field: 'client_secret',
    label: 'Client Secret',
    before: undefined,
    after: persistedSecrets.clientSecret
  }, {
    field: 'identity_token',
    label: 'Identity Token',
    before: undefined,
    after: persistedSecrets.wifJwt
  }, {
    field: 'password',
    label: '代理密码',
    before: undefined,
    after: persistedSecrets.proxyPassword
  }, {
    field: 'tokenPreview',
    label: 'Token 标识',
    before: undefined,
    after: 'prefix...suffix'
  }, {
    field: 'notes',
    label: '说明',
    before: '',
    after: ordinaryText
  }], 100)
  operationLogQueue.enqueueOperationLogsLocal([{
    ...buildOperationLog('sensitive_container_redaction'),
    id: 'oplog_sensitive_container_redaction',
    changes: sensitiveChanges
  }])
  operationLogQueue.flushAllOperationLogQueue()
  const sensitiveDetail = repositories.getOperationLogDetail('oplog_sensitive_container_redaction')
  assert(sensitiveDetail, '敏感容器操作日志必须完成真实持久化')
  const persistedDetailText = JSON.stringify(sensitiveDetail)
  for (const secret of Object.values(persistedSecrets)) {
    assert.equal(persistedDetailText.includes(secret), false, `操作日志持久化不得包含认证秘密：${secret.slice(0, 12)}`)
  }
  assert.equal(sensitiveDetail.changes.find((change) => change.field === 'credentials')?.after, '已变更', 'credentials 容器只记录状态摘要')
  assert.equal(sensitiveDetail.changes.find((change) => change.field === 'notes')?.after, ordinaryText, '普通文本不得按敏感关键词做任意正则扫描')
  assert.equal(sensitiveDetail.changes.find((change) => change.field === 'tokenPreview')?.after, 'prefix...suffix', '精确 allowlist 不得误伤 tokenPreview 摘要字段')

  let failedInsertPrepares = 0
  const failuresBefore = operationLogQueue.getOperationLogQueueRuntime().flushFailureCount
  database.prepare = ((sql: string) => {
    if (/^\s*INSERT\s+INTO\s+operation_logs\b/i.test(sql)) {
      failedInsertPrepares += 1
      if (failedInsertPrepares === 1) {
        throw new Error('模拟操作日志批量写入失败')
      }
    }
    return originalPrepare(sql)
  }) as typeof database.prepare
  try {
    operationLogQueue.enqueueOperationLogsLocal([buildOperationLog('retry_disabled_guard')])
    operationLogQueue.flushOperationLogQueue({ retryOnFailure: false })
    assert.equal(operationLogQueue.getOperationLogQueueRuntime().flushFailureCount, failuresBefore + 1, '操作日志写入失败应记录 flush 失败')
    assert.equal(operationLogQueue.getOperationLogQueueRuntime().queueLength, 1, 'retryOnFailure=false 时失败操作日志应保留在队列')
    await waitForImmediate()
    assert.equal(operationLogQueue.getOperationLogQueueRuntime().queueLength, 1, 'retryOnFailure=false 不应在返回后立刻异步重试操作日志')
    await waitForRetryDelay()
    assert.equal(operationLogQueue.getOperationLogQueueRuntime().queueLength, 1, 'retryOnFailure=false 不应在默认重试延迟后异步重试操作日志')
    assert.equal(operationLogExists('retry_disabled_guard'), 0, '失败后操作日志不应被后台定时器偷偷写入')
  } finally {
    database.prepare = originalPrepare
  }
  operationLogQueue.flushAllOperationLogQueue()
  assert.equal(operationLogQueue.getOperationLogQueueRuntime().queueLength, 0, '恢复后保留的操作日志应可继续 flush 完成')
  assert.equal(operationLogExists('retry_disabled_guard'), 1, '恢复后应写入保留的操作日志')

  const operationLogWriteSource = readFileSync(new URL('../../storage/operation-log-write.repository.ts', import.meta.url), 'utf8')
  assert.match(operationLogWriteSource, /insertPostgresOperationLogsBatch\(tx,\s*preparedLogs\)/, 'PG 操作日志批量写入应一次处理主表 rows')
  assert.match(operationLogWriteSource, /const insertedLogIds = await insertPostgresOperationLogsBatch/, 'PG 操作日志批量写入应识别本次实际新插入的日志')
  assert.match(operationLogWriteSource, /ON CONFLICT\(id\) DO NOTHING[\s\S]*RETURNING id/, 'PG 操作日志主表写入应支持 Redis Stream 重投幂等')
  assert.match(operationLogWriteSource, /preparedLogs\.filter\(\(prepared\) => insertedLogIds\.has\(prepared\.id\)\)/, 'PG 操作日志子表写入应只处理本次新插入的日志，避免重投重复写子表')
  assert.match(operationLogWriteSource, /insertPostgresOperationLogSearchTermsBatch\(tx,\s*insertedLogs\)/, 'PG 操作日志搜索词应走批量分块写入')
  assert.match(operationLogWriteSource, /postgresOperationLogSearchTermRowsPerInsert/, 'PG 操作日志搜索词批量写入应有参数上限保护')
  assert.doesNotMatch(operationLogWriteSource, /for\s*\(\s*const prepared of preparedLogs\s*\)\s*\{\s*await insertPreparedOperationLogPostgres/, 'PG 操作日志批量写入不能退回逐条日志写入')

  console.log('操作日志队列回归通过：写入边界正确，批量落库复用 prepared statements')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
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
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT COUNT(*) AS total FROM operation_logs')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function operationLogTargetCount(): number {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT COUNT(*) AS total FROM operation_log_targets')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function operationLogViewerCount(): number {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT COUNT(*) AS total FROM operation_log_viewers')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function operationLogExists(action: string): number {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT COUNT(*) AS total FROM operation_logs WHERE action = ?')
    .get(action) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

async function waitForImmediate(): Promise<void> {
  await new Promise<void>((resolvePromise) => setImmediate(resolvePromise))
}

async function waitForRetryDelay(): Promise<void> {
  await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 1100))
}
