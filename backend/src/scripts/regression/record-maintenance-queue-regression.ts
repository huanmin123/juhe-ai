import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-record-maintenance-queue-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  recordMaintenanceQueue,
  backgroundIpc
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/record-maintenance/record-maintenance-queue.service.js'),
  import('../../modules/background/background-ipc.js')
])

try {
  runtimeConfig.processRole = 'worker'
  const completedBefore = recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().completedCount
  recordMaintenanceQueue.enqueueRecordMaintenanceJob(buildUsageRecordsCleanupJob('worker_local'))
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().queueLength, 1, 'worker 角色应进入本地记录库维护队列')
  recordMaintenanceQueue.flushAllRecordMaintenanceQueue()
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().queueLength, 0, 'worker flush 后记录库维护队列应清空')
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().completedCount, completedBefore + 1, 'worker flush 应执行记录库维护任务')

  seedUsageRecord('usage_cleanup_regression', '2000-01-01T00:00:00.000Z')
  recordMaintenanceQueue.enqueueRecordMaintenanceJob({
    type: 'usage_records_cleanup',
    id: 'recmaint_usage_cleanup_regression',
    cutoffAt: '2000-01-02T00:00:00.000Z',
    batchSize: 100,
    maxBatches: 1
  })
  recordMaintenanceQueue.flushAllRecordMaintenanceQueue()
  assert.equal(usageRecordCount('usage_cleanup_regression'), 0, 'worker 记录库维护任务应真实删除截止时间前的使用记录')

  seedUsageRecord('usage_cleanup_recent_protected', new Date().toISOString())
  recordMaintenanceQueue.enqueueRecordMaintenanceJob({
    type: 'usage_records_cleanup',
    id: 'recmaint_usage_cleanup_recent_protected',
    cutoffAt: new Date(Date.now() + 60 * 1000).toISOString(),
    batchSize: 100,
    maxBatches: 1
  })
  recordMaintenanceQueue.flushAllRecordMaintenanceQueue()
  assert.equal(usageRecordCount('usage_cleanup_recent_protected'), 1, 'worker 记录库维护任务应强制保留最近 1 天的使用记录')

  seedAccount('acct_codex_snapshot', 'sys_admin')
  recordMaintenanceQueue.enqueueRecordMaintenanceJob({
    type: 'account_usage_snapshot_upsert',
    id: 'recmaint_account_usage_snapshot',
    accountId: 'acct_codex_snapshot',
    kind: 'openai_codex',
    source: 'regression',
    snapshot: {
      codex_usage_updated_at: '2000-01-01T00:00:00.000Z',
      codex_5h_used_percent: 12
    },
    updatedAt: '2000-01-01T00:00:00.000Z'
  })
  recordMaintenanceQueue.flushAllRecordMaintenanceQueue()
  assert.equal(accountUsageSnapshotCount('acct_codex_snapshot'), 1, 'worker 应能通过记录库维护队列写入账号用量快照')

  for (let index = 0; index < 5; index += 1) {
    seedAccount(`acct_codex_snapshot_batch_${index}`, 'sys_admin')
  }
  const businessDatabase = databaseModule.getDatabase()
  const recordDatabase = databaseModule.getRecordDatabase()
  const originalBusinessPrepare = businessDatabase.prepare.bind(businessDatabase) as typeof businessDatabase.prepare
  const originalRecordPrepare = recordDatabase.prepare.bind(recordDatabase) as typeof recordDatabase.prepare
  let accountOwnerBatchSelects = 0
  let accountUsageSnapshotUpsertPrepares = 0
  businessDatabase.prepare = ((sql: string) => {
    if (/^\s*SELECT\s+id,\s*system_account_id\s+FROM\s+accounts\s+WHERE\s+id\s+IN\s*\(/i.test(sql)) {
      accountOwnerBatchSelects += 1
    }
    if (/^\s*SELECT\s+system_account_id\s+FROM\s+accounts\s+WHERE\s+id\s+=\s+\?/i.test(sql)) {
      throw new Error('批量账号用量快照写入不应逐账号查询归属')
    }
    return originalBusinessPrepare(sql)
  }) as typeof businessDatabase.prepare
  recordDatabase.prepare = ((sql: string) => {
    if (/^\s*INSERT\s+INTO\s+account_usage_snapshots\b/i.test(sql)) {
      accountUsageSnapshotUpsertPrepares += 1
    }
    return originalRecordPrepare(sql)
  }) as typeof recordDatabase.prepare

  try {
    recordMaintenanceQueue.enqueueRecordMaintenanceJobsLocal(Array.from({ length: 5 }, (_, index) => ({
      type: 'account_usage_snapshot_upsert' as const,
      id: `recmaint_account_usage_snapshot_batch_${index}`,
      accountId: `acct_codex_snapshot_batch_${index}`,
      kind: 'openai_codex' as const,
      source: 'regression_batch',
      snapshot: {
        codex_usage_updated_at: '2000-01-01T00:00:00.000Z',
        codex_5h_used_percent: 20 + index
      },
      updatedAt: '2000-01-01T00:00:00.000Z'
    })))
    recordMaintenanceQueue.flushAllRecordMaintenanceQueue()
  } finally {
    businessDatabase.prepare = originalBusinessPrepare
    recordDatabase.prepare = originalRecordPrepare
  }
  assert.equal(accountOwnerBatchSelects, 1, '连续账号用量快照 job 应批量读取账号归属')
  assert.equal(accountUsageSnapshotUpsertPrepares, 1, '连续账号用量快照 job 应复用 upsert statement')
  for (let index = 0; index < 5; index += 1) {
    assert.equal(accountUsageSnapshotCount(`acct_codex_snapshot_batch_${index}`), 1, `批量账号用量快照应写入账号 ${index}`)
  }

  runtimeConfig.processRole = 'server'
  const pendingBefore = backgroundIpc.getBackgroundWorkerState().pendingMessageCount
  recordMaintenanceQueue.enqueueRecordMaintenanceJob(buildUsageRecordsCleanupJob('server_ipc'))
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().queueLength, 0, 'server 角色不能进入本地记录库维护队列')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, pendingBefore + 1, 'server 角色应把记录库维护任务投递到 worker IPC 队列')

  runtimeConfig.processRole = 'db-service'
  const droppedBefore = recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().droppedCount
  recordMaintenanceQueue.enqueueRecordMaintenanceJob(buildApiKeyCleanupJob('db_service_parent_ipc_missing'))
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().queueLength, 0, 'db-service 角色不能进入本地记录库维护队列')
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().droppedCount, droppedBefore + 1, '无父进程 IPC 的 db-service 测试态应记录投递失败计数')

  console.log('记录库维护队列回归通过：server/db-service 只投递，worker 才执行记录库清理')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function buildUsageRecordsCleanupJob(source: string) {
  return {
    type: 'usage_records_cleanup' as const,
    id: `recmaint_${source}`,
    cutoffAt: '2000-01-01T00:00:00.000Z',
    batchSize: 100,
    maxBatches: 1
  }
}

function buildApiKeyCleanupJob(source: string) {
  return {
    type: 'api_key_related_cleanup' as const,
    id: `recmaint_${source}`,
    apiKeyId: `key_${source}`,
    systemAccountId: 'sys_admin'
  }
}

function seedAccount(accountId: string, systemAccountId: string): void {
  databaseModule.getDatabase()
    .prepare(`
      INSERT OR IGNORE INTO providers (id, code, name, description, enabled, base_url, account_types_json, capabilities_json, created_at, updated_at)
      VALUES ('prov_openai', 'openai', 'OpenAI', NULL, 1, 'https://api.openai.com', '[]', '{}', ?, ?)
    `)
    .run('2000-01-01T00:00:00.000Z', '2000-01-01T00:00:00.000Z')
  databaseModule.getDatabase()
    .prepare(`
      INSERT INTO accounts (id, system_account_id, provider_code, name, type, status, credentials_encrypted, schedulable, created_at, updated_at)
      VALUES (?, ?, 'openai', ?, 'oauth', 'active', ?, 1, ?, ?)
    `)
    .run(accountId, systemAccountId, accountId, '{}', '2000-01-01T00:00:00.000Z', '2000-01-01T00:00:00.000Z')
}

function seedUsageRecord(id: string, createdAt: string): void {
  databaseModule.getRecordDatabase()
    .prepare(`
      INSERT INTO usage_records (id, system_account_id, trace_id, stream, success, created_at)
      VALUES (?, 'sys_admin', ?, 0, 1, ?)
    `)
    .run(id, `trace_${id}`, createdAt)
}

function usageRecordCount(id: string): number {
  const row = databaseModule.getRecordDatabase()
    .prepare('SELECT COUNT(*) AS total FROM usage_records WHERE id = ?')
    .get(id) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function accountUsageSnapshotCount(accountId: string): number {
  const row = databaseModule.getRecordDatabase()
    .prepare('SELECT COUNT(*) AS total FROM account_usage_snapshots WHERE account_id = ?')
    .get(accountId) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}
