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

function accountUsageSnapshotCount(accountId: string): number {
  const row = databaseModule.getRecordDatabase()
    .prepare('SELECT COUNT(*) AS total FROM account_usage_snapshots WHERE account_id = ?')
    .get(accountId) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}
