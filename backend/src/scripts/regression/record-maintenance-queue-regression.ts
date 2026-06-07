import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

assertRecordMaintenanceCleanupRunsAsync()

const tempRoot = resolve(tmpdir(), `juhe-ai-record-maintenance-queue-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  recordMaintenanceQueue,
  backgroundIpc,
  usageRecordShards
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/record-maintenance/record-maintenance-queue.service.js'),
  import('../../modules/background/background-ipc.js'),
  import('../../storage/usage-record-shards.js')
])

try {
  runtimeConfig.processRole = 'worker'
  const completedBefore = recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().completedCount
  recordMaintenanceQueue.enqueueRecordMaintenanceJob(buildUsageRecordsCleanupJob('worker_local'))
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().queueLength, 1, 'worker 角色应进入本地数据维护队列')
  await recordMaintenanceQueue.flushAllRecordMaintenanceQueue()
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().queueLength, 0, 'worker flush 后数据维护队列应清空')
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().completedCount, completedBefore + 1, 'worker flush 应执行数据维护任务')

  seedUsageRecord('usage_cleanup_regression', '2000-01-01T00:00:00.000Z')
  recordMaintenanceQueue.enqueueRecordMaintenanceJob({
    type: 'usage_records_cleanup',
    id: 'recmaint_usage_cleanup_regression',
    cutoffAt: '2000-01-02T00:00:00.000Z',
    batchSize: 100,
    maxBatches: 1
  })
  await recordMaintenanceQueue.flushAllRecordMaintenanceQueue()
  assert.equal(usageRecordCount('usage_cleanup_regression'), 1, '统计安全游标未就绪时不应删除使用记录')

  seedUsageStatsCleanupCursors('2000-01-01T00:00:01.000Z', 'usage_cleanup_regression')
  recordMaintenanceQueue.enqueueRecordMaintenanceJob({
    type: 'usage_records_cleanup',
    id: 'recmaint_usage_cleanup_regression_after_cursor',
    cutoffAt: '2000-01-02T00:00:00.000Z',
    batchSize: 100,
    maxBatches: 1
  })
  await recordMaintenanceQueue.flushAllRecordMaintenanceQueue()
  assert.equal(usageRecordCount('usage_cleanup_regression'), 0, '统计安全游标就绪后才允许删除已聚合使用记录')

  seedUsageRecord('usage_cleanup_recent_protected', new Date().toISOString())
  recordMaintenanceQueue.enqueueRecordMaintenanceJob({
    type: 'usage_records_cleanup',
    id: 'recmaint_usage_cleanup_recent_protected',
    cutoffAt: new Date(Date.now() + 60 * 1000).toISOString(),
    batchSize: 100,
    maxBatches: 1
  })
  await recordMaintenanceQueue.flushAllRecordMaintenanceQueue()
  assert.equal(usageRecordCount('usage_cleanup_recent_protected'), 1, 'worker 数据维护任务应强制保留最近 1 天的使用记录')

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
  await recordMaintenanceQueue.flushAllRecordMaintenanceQueue()
  assert.equal(accountUsageSnapshotCount('acct_codex_snapshot'), 1, 'worker 应能通过数据维护队列写入账号用量快照')

  for (let index = 0; index < 5; index += 1) {
    seedAccount(`acct_codex_snapshot_batch_${index}`, 'sys_admin')
  }
  const businessDatabase = databaseModule.getBusinessDatabase()
  const statsDatabase = databaseModule.getStatsDatabase()
  const originalBusinessPrepare = businessDatabase.prepare.bind(businessDatabase) as typeof businessDatabase.prepare
  const originalStatsPrepare = statsDatabase.prepare.bind(statsDatabase) as typeof statsDatabase.prepare
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
  statsDatabase.prepare = ((sql: string) => {
    if (/^\s*INSERT\s+INTO\s+account_usage_snapshots\b/i.test(sql)) {
      accountUsageSnapshotUpsertPrepares += 1
    }
    return originalStatsPrepare(sql)
  }) as typeof statsDatabase.prepare

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
    await recordMaintenanceQueue.flushAllRecordMaintenanceQueue()
  } finally {
    businessDatabase.prepare = originalBusinessPrepare
    statsDatabase.prepare = originalStatsPrepare
  }
  assert.equal(accountOwnerBatchSelects, 1, '连续账号用量快照 job 应批量读取账号归属')
  assert.equal(accountUsageSnapshotUpsertPrepares, 1, '连续账号用量快照 job 应复用 upsert statement')
  for (let index = 0; index < 5; index += 1) {
    assert.equal(accountUsageSnapshotCount(`acct_codex_snapshot_batch_${index}`), 1, `批量账号用量快照应写入账号 ${index}`)
  }

  seedAccount('acct_codex_snapshot_retry_0', 'sys_admin')
  seedAccount('acct_codex_snapshot_retry_1', 'sys_admin')
  seedUsageRecord('usage_cleanup_retry_guard', '2000-01-01T00:00:00.000Z')
  seedUsageStatsCleanupCursors('2000-01-01T00:00:01.000Z', 'usage_cleanup_retry_guard')
  let failedSnapshotUpsertPrepares = 0
  statsDatabase.prepare = ((sql: string) => {
    if (/^\s*INSERT\s+INTO\s+account_usage_snapshots\b/i.test(sql)) {
      failedSnapshotUpsertPrepares += 1
      if (failedSnapshotUpsertPrepares === 1) {
        throw new Error('模拟账号用量快照批量写入失败')
      }
    }
    return originalStatsPrepare(sql)
  }) as typeof statsDatabase.prepare
  const failuresBefore = recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().flushFailureCount
  try {
    recordMaintenanceQueue.enqueueRecordMaintenanceJobsLocal([
      buildAccountUsageSnapshotJob('recmaint_account_usage_snapshot_retry_0', 'acct_codex_snapshot_retry_0', 31),
      buildAccountUsageSnapshotJob('recmaint_account_usage_snapshot_retry_1', 'acct_codex_snapshot_retry_1', 32),
      {
        type: 'usage_records_cleanup' as const,
        id: 'recmaint_usage_cleanup_retry_guard',
        cutoffAt: '2000-01-02T00:00:00.000Z',
        batchSize: 100,
        maxBatches: 1
      }
    ])
    await recordMaintenanceQueue.flushRecordMaintenanceQueue({ retryOnFailure: false })
    assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().flushFailureCount, failuresBefore + 1, '批量快照写入失败应记录 flush 失败')
    assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().queueLength, 3, 'retryOnFailure=false 时失败任务和同批后续任务应保留在队列')
    await waitForImmediate()
    assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().queueLength, 3, 'retryOnFailure=false 不应在返回后立刻异步重试')
    await waitForRetryDelay()
    assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().queueLength, 3, 'retryOnFailure=false 不应在默认重试延迟后异步重试')
    assert.equal(accountUsageSnapshotCount('acct_codex_snapshot_retry_0'), 0, '失败事务不应写入部分账号快照')
    assert.equal(accountUsageSnapshotCount('acct_codex_snapshot_retry_1'), 0, '失败事务不应写入批量快照中的后续账号')
    assert.equal(usageRecordCount('usage_cleanup_retry_guard'), 1, '失败后不应越过失败任务执行后续清理')
  } finally {
    statsDatabase.prepare = originalStatsPrepare
  }
  await recordMaintenanceQueue.flushAllRecordMaintenanceQueue()
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().queueLength, 0, '恢复后保留任务应可继续 flush 完成')
  assert.equal(accountUsageSnapshotCount('acct_codex_snapshot_retry_0'), 1, '恢复后应写入失败前的第一个快照任务')
  assert.equal(accountUsageSnapshotCount('acct_codex_snapshot_retry_1'), 1, '恢复后应写入失败前的第二个快照任务')
  assert.equal(usageRecordCount('usage_cleanup_retry_guard'), 0, '恢复后后续清理任务应继续按顺序执行')

  runtimeConfig.processRole = 'server'
  const pendingBefore = backgroundIpc.getBackgroundWorkerState().pendingMessageCount
  recordMaintenanceQueue.enqueueRecordMaintenanceJob(buildUsageRecordsCleanupJob('server_ipc'))
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().queueLength, 0, 'server 角色不能进入本地数据维护队列')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, pendingBefore + 1, 'server 角色应把数据维护任务投递到 worker IPC 队列')

  runtimeConfig.processRole = 'db-service'
  const droppedBefore = recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().droppedCount
  recordMaintenanceQueue.enqueueRecordMaintenanceJob(buildApiKeyCleanupJob('db_service_parent_ipc_missing'))
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().queueLength, 0, 'db-service 角色不能进入本地数据维护队列')
  assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().droppedCount, droppedBefore + 1, '无父进程 IPC 的 db-service 测试态应记录投递失败计数')

  const originalProcessSend = process.send
  try {
    process.send = ((message: unknown, callback?: (error: Error | null) => void) => {
      void message
      callback?.(new Error('模拟父进程 IPC 异步失败'))
      return true
    }) as NodeJS.Process['send']
    const asyncDroppedBefore = recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().droppedCount
    const asyncDispatchResult = recordMaintenanceQueue.enqueueRecordMaintenanceJobWithResult(buildApiKeyCleanupJob('db_service_parent_ipc_async_failed'))
    assert.equal(asyncDispatchResult.queued, true, 'DB service 父进程 IPC 异步失败前同步投递结果仍只能表示已尝试发送')
    await waitForImmediate()
    assert.equal(recordMaintenanceQueue.getRecordMaintenanceQueueRuntime().droppedCount, asyncDroppedBefore + 1, 'DB service 父进程 IPC 异步失败应记录投递失败计数')
  } finally {
    process.send = originalProcessSend
  }

  console.log('数据维护队列回归通过：server/db-service 只投递，worker 才执行数据清理')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
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

function buildAccountUsageSnapshotJob(id: string, accountId: string, usedPercent: number) {
  return {
    type: 'account_usage_snapshot_upsert' as const,
    id,
    accountId,
    kind: 'openai_codex' as const,
    source: 'regression_retry_guard',
    snapshot: {
      codex_usage_updated_at: '2000-01-01T00:00:00.000Z',
      codex_5h_used_percent: usedPercent
    },
    updatedAt: '2000-01-01T00:00:00.000Z'
  }
}

function assertRecordMaintenanceCleanupRunsAsync(): void {
  const queueSource = readFileSync(new URL('../../modules/record-maintenance/record-maintenance-queue.service.ts', import.meta.url), 'utf8')
  assert(queueSource.includes('cleanupDeletedApiKeyRelatedRecordDataAsync'), '数据维护队列应使用 API Key 异步清理入口')
  assert(queueSource.includes('cleanupDeletedAccountRelatedRecordDataAsync'), '数据维护队列应使用 AI 账户异步清理入口')
  assert(!/cleanupDeleted(ApiKey|Account)RelatedRecordData\(\{/.test(queueSource), '数据维护队列不应回退到同步已删除记录清理入口')

  const backgroundSource = readFileSync(new URL('../../modules/background/background-jobs.ts', import.meta.url), 'utf8')
  assert(backgroundSource.includes('cleanupPendingDeletedApiKeyRecordTargetsAsync'), '后台 API Key 清理重试应使用异步入口')
  assert(backgroundSource.includes('cleanupPendingDeletedAccountRecordTargetsAsync'), '后台 AI 账户清理重试应使用异步入口')
}

function seedAccount(accountId: string, systemAccountId: string): void {
  databaseModule.getBusinessDatabase()
    .prepare(`
      INSERT INTO accounts (
        id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
        name, type, status, credentials_encrypted, schedulable, created_at, updated_at
      )
      VALUES (?, ?, 'gpt', 'profile_gpt_openai_v1', 'openai', 'v1', ?, 'oauth', 'active', ?, 1, ?, ?)
    `)
    .run(accountId, systemAccountId, accountId, '{}', '2000-01-01T00:00:00.000Z', '2000-01-01T00:00:00.000Z')
}

function seedUsageRecord(id: string, createdAt: string): void {
  const location = usageRecordShards.usageRecordShardLocationForRecord(id, createdAt)
  usageRecordShards.getUsageRecordShardDatabase(location)
    .prepare(`
      INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, stream, success, created_at)
      VALUES (?, 'sys_admin', ?, 'gateway', 0, 1, ?)
    `)
    .run(id, `trace_${id}`, createdAt)
  usageRecordShards.recordUsageRecordShardEntries([{
    id,
    shardKey: location.shardKey,
    systemAccountId: 'sys_admin',
    trafficSource: 'gateway',
    success: true,
    createdAt
  }])
}

function seedUsageStatsCleanupCursors(cursorCreatedAt: string, cursorId: string): void {
  const database = databaseModule.getStatsDatabase()
  const location = usageRecordShards.usageRecordShardLocationForRecord(cursorId, cursorCreatedAt)
  const statement = database.prepare(`
    INSERT INTO stats_job_state (
      scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, updated_at
    ) VALUES ('usage_shard', ?, ?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = excluded.cursor_created_at,
      cursor_id = excluded.cursor_id,
      last_success_at = excluded.last_success_at,
      updated_at = excluded.updated_at
  `)
  for (const jobName of ['usage_stats_aggregation', 'client_ip_stats_aggregation']) {
    statement.run(location.shardKey, jobName, cursorCreatedAt, cursorId, cursorCreatedAt, cursorCreatedAt)
  }
}

function usageRecordCount(id: string): number {
  return usageRecordShards.listUsageRecordShardLocations()
    .reduce((total, location) => {
      const row = usageRecordShards.getUsageRecordShardDatabase(location)
        .prepare('SELECT COUNT(*) AS total FROM usage_records WHERE id = ?')
        .get(id) as { total?: number } | undefined
      return total + Number(row?.total ?? 0)
    }, 0)
}

function accountUsageSnapshotCount(accountId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare('SELECT COUNT(*) AS total FROM account_usage_snapshots WHERE account_id = ?')
    .get(accountId) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

async function waitForImmediate(): Promise<void> {
  await new Promise<void>((resolvePromise) => setImmediate(resolvePromise))
}

async function waitForRetryDelay(): Promise<void> {
  await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 1100))
}
