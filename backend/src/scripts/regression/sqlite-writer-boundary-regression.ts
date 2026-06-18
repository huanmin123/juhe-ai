import { strict as assert } from 'node:assert'
import type { ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = 'true'

const tempRoot = resolve(tmpdir(), `juhe-ai-sqlite-writer-boundary-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'sqlite-writer-boundary-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const databaseModule = await import('../../storage/database.js')
const usageRecordShards = await import('../../storage/usage-record-shards.js')

try {
  assert.equal(databaseModule.sqliteWriterOwnerForMainDatabase('business'), 'db-service')
  assert.equal(databaseModule.sqliteWriterOwnerForMainDatabase('dataset'), 'ingest-worker')
  assert.equal(databaseModule.sqliteWriterOwnerForMainDatabase('stats'), 'stats-writer')
  assert.equal(databaseModule.sqliteWriterBoundaryStrictModeEnabled(), true)

  runtimeConfig.processRole = 'db-service'
  runtimeConfig.workerRole = 'worker'
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('business'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('dataset'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('stats'), false)
  assert.equal(usageRecordShards.currentProcessOwnsUsageShardWriter(), false)
  databaseModule.getBusinessDatabase()
  databaseModule.closeStorageDatabases()

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'ingest-worker'
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('business'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('dataset'), true)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('stats'), false)
  assert.equal(usageRecordShards.currentProcessOwnsUsageShardWriter(), true)
  databaseModule.getDatasetDatabase()
  const shardLocation = usageRecordShards.usageRecordShardLocationForRecord('usage_20260618_s00_boundary', '2026-06-18T00:00:00.000Z')
  usageRecordShards.getUsageRecordShardDatabase(shardLocation)
  databaseModule.closeStorageDatabases()

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'stats-worker'
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('business'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('dataset'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('stats'), true)
  assert.equal(usageRecordShards.currentProcessOwnsUsageShardWriter(), false)
  databaseModule.getStatsDatabase()
  databaseModule.closeStorageDatabases()

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'maintenance-worker'
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('business'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('dataset'), false)
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('stats'), false)
  assert.equal(usageRecordShards.currentProcessOwnsUsageShardWriter(), false)
  assertNonOwnerWriteBlocked(databaseModule.getBusinessDatabase(), '业务库')
  assertNonOwnerWriteBlocked(databaseModule.getDatasetDatabase(), '数据集目录库')
  assertNonOwnerWriteBlocked(databaseModule.getStatsDatabase(), '统计库')
  assertNonOwnerWriteBlocked(usageRecordShards.getUsageRecordShardDatabase(shardLocation), 'usage shard')
  assertRuntimeWriteQueueSourceGuards()
  await assertDatasetWriterBridge()

  console.log('SQLite writer boundary 回归通过：主库 / usage shard owner 划分、默认严格模式、非 owner 写入只读保护和 dataset writer 转发边界已就绪')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertNonOwnerWriteBlocked(database: import('node:sqlite').DatabaseSync, label: string): void {
  const queryOnly = database.prepare('PRAGMA query_only').get() as { query_only?: number } | undefined
  assert.equal(queryOnly?.query_only, 1, `${label} 非 owner 连接必须启用 PRAGMA query_only`)
  assert.throws(
    () => database.exec('CREATE TABLE __sqlite_writer_boundary_blocked(id TEXT PRIMARY KEY)'),
    /attempt to write a readonly database|readonly|query_only|SQLITE_READONLY/i,
    `${label} 非 owner 连接必须拒绝写 SQL`
  )
}

function assertRuntimeWriteQueueSourceGuards(): void {
  const modelCheckServiceSource = readFileSync(resolve('src/modules/model-checks/model-checks.service.ts'), 'utf8')
  assert(modelCheckServiceSource.includes('requestDatasetWriter'), '模型检测运行时写入必须通过 dataset writer')
  for (const forbidden of ['createModelCheckRun', 'createModelCheckItems', 'finishModelCheckRun']) {
    assert.equal(
      modelCheckServiceSource.includes(forbidden),
      false,
      `模型检测 service 不能直接调用数据集库写 repository：${forbidden}`
    )
  }

  const datasetWriterSource = readFileSync(resolve('src/modules/background/background-dataset-writer.ts'), 'utf8')
  for (const required of ['create_model_check_run', 'create_model_check_items', 'finish_model_check_run']) {
    assert(datasetWriterSource.includes(required), `dataset writer 必须登记模型检测写操作：${required}`)
  }

  const workerSource = readFileSync(resolve('src/worker.ts'), 'utf8')
  assert(workerSource.includes('background_worker_dataset_write_request'), 'ingest-worker 必须消费 dataset writer 请求')
  assert(workerSource.includes('handleDatasetWriteOperation'), 'ingest-worker 必须通过 dataset writer handler 执行数据集写入')

  const dbServiceIpcSource = readFileSync(resolve('src/modules/db-service/db-service-ipc.ts'), 'utf8')
  assert(dbServiceIpcSource.includes('requestDbServiceDatasetWrite'), 'DB service 必须具备 dataset writer 转发入口')
  assert(dbServiceIpcSource.includes('respondToDatasetWriteRequest'), 'server 必须把 DB service dataset writer 请求转发给 ingest-worker')

  const runtimeLogQueueSource = readFileSync(resolve('src/modules/runtime-logs/runtime-log-index-queue.service.ts'), 'utf8')
  assert(runtimeLogQueueSource.includes("runtimeConfig.processRole === 'db-service'"), 'DB service 运行日志索引不能回退到本地 dataset 队列')
  assert(runtimeLogQueueSource.includes('sendRuntimeLogLineFromDbServiceToServer'), 'DB service 运行日志索引必须投递父进程后再由 ingest-worker 写入')
}

async function assertDatasetWriterBridge(): Promise<void> {
  runtimeConfig.processRole = 'server'
  runtimeConfig.workerRole = 'worker'
  const backgroundIpc = await import('../../modules/background/background-ipc.js')
  const fakeIngestWorker = createFakeDatasetWriterWorkerProcess(56048)
  backgroundIpc.attachBackgroundWorkerProcess(fakeIngestWorker as unknown as ChildProcess, { role: 'ingest-worker' })
  fakeIngestWorker.ready()

  const result = await backgroundIpc.requestBackgroundWorkerDatasetWrite({
    type: 'create_model_check_run',
    input: {
      id: 'model_check_writer_bridge_run',
      systemAccountId: 'sys_admin',
      actorSystemAccountId: 'sys_admin',
      providerCode: 'gpt',
      targetType: 'account',
      targetId: 'acct_writer_bridge',
      targetName: 'writer bridge account',
      accountId: 'acct_writer_bridge',
      model: 'gpt-5.5',
      profile: 'full',
      trustedComparison: false,
      trustedComparisonAvailable: false,
      probeSetVersion: 'regression',
      startedAt: '2000-01-01T00:00:00.000Z'
    }
  }, 1000) as { id?: string } | undefined

  assert.equal(result?.id, 'model_check_writer_bridge_run', 'server dataset writer 请求必须由 ingest-worker 回包')
  assert.equal(fakeIngestWorker.datasetWriteRequestCount, 1, 'dataset writer 请求必须投递到 ingest-worker IPC')
  fakeIngestWorker.exit()
}

function createFakeDatasetWriterWorkerProcess(pid: number) {
  class FakeDatasetWriterWorkerProcess extends EventEmitter {
    connected = true
    killed = false
    datasetWriteRequestCount = 0

    constructor(public readonly pid: number) {
      super()
    }

    send(message: unknown, callback?: (error?: Error | null) => void): boolean {
      callback?.(null)
      if (isDatasetWriteRequest(message)) {
        this.datasetWriteRequestCount += 1
        setImmediate(() => {
          this.emit('message', {
            type: 'background_worker_dataset_write_response',
            requestId: message.requestId,
            ok: true,
            result: { id: message.operation.input.id }
          })
        })
      }
      return true
    }

    kill(): boolean {
      this.killed = true
      this.connected = false
      return true
    }

    ready(): void {
      this.emit('message', { type: 'background_worker_ready', pid: this.pid, workerRole: 'ingest-worker' })
    }

    exit(): void {
      this.connected = false
      this.emit('exit', 0, null)
    }
  }

  return new FakeDatasetWriterWorkerProcess(pid)
}

function isDatasetWriteRequest(message: unknown): message is {
  type: 'background_worker_dataset_write_request'
  requestId: string
  operation: { type: 'create_model_check_run'; input: { id?: string } }
} {
  return typeof message === 'object'
    && message !== null
    && (message as { type?: unknown }).type === 'background_worker_dataset_write_request'
    && typeof (message as { requestId?: unknown }).requestId === 'string'
    && typeof (message as { operation?: { type?: unknown } }).operation === 'object'
    && (message as { operation?: { type?: unknown } }).operation?.type === 'create_model_check_run'
}
