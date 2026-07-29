import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

function source(path: string): string {
  return readFileSync(new URL(path, import.meta.url), 'utf8')
}

const repositorySource = source('../../storage/runtime-logs.repository.ts')
const ipcTypesSource = source('../../modules/background/background-ipc.types.ts')
const queueHealthSource = source('../../modules/background/background-queue-health.service.ts')
const statsRoutesSource = source('../../modules/stats/stats.routes.ts')
const frontendTypesSource = source('../../../../frontend/src/types/domain/runtime-logs.ts')

const fileRuntimeFields = [
  'discoveredFileCount',
  'pendingFileCount',
  'pendingBytes',
  'oldestPendingMtime',
  'currentFile',
  'currentOffset',
  'lastReadAt',
  'lastCommitAt',
  'lastError',
  'protectedRotatedFileCount'
]

for (const field of fileRuntimeFields) {
  assert.match(ipcTypesSource, new RegExp(`\\b${field}\\b`), `后台 IPC 运行日志契约必须暴露 ${field}`)
  assert.doesNotMatch(frontendTypesSource, new RegExp(`\\b${field}\\b`), `前端运行日志页面不得暴露无人消费的 ${field}`)
}

for (const redisField of [
  'redisEnqueueInFlightCount',
  'redisEnqueueInFlightBytes',
  'redisEnqueueTimeoutDropCount',
  'redisEnqueueDisconnectedDropCount',
  'redisEnqueueCommandFailureDropCount'
]) {
  assert.doesNotMatch(ipcTypesSource, new RegExp(`\\b${redisField}\\b`), `公共后台运行日志契约不得暴露 ${redisField}`)
  assert.doesNotMatch(frontendTypesSource, new RegExp(`\\b${redisField}\\b`), `前端运行日志契约不得暴露 ${redisField}`)
}

assert.match(repositorySource, /export async function getRuntimeLogFileCursorAsync\(/, '运行日志游标必须提供 async 读取入口')
assert.match(repositorySource, /export async function upsertRuntimeLogFileCursorAsync\(/, '运行日志游标必须提供 async 写入入口')
assert.match(repositorySource, /export interface RuntimeLogFileCursorAsyncDependencies/, 'async 游标 PostgreSQL 分支必须提供可控 client/时钟依赖端口')
assert.match(repositorySource, /runtimeConfig\.databaseDriver !== 'postgres'[\s\S]+getRuntimeLogFileCursor\(logFile\)/, 'SQLite async 游标读取必须复用同步语义')
assert.match(repositorySource, /SELECT \* FROM juhe_dataset\.runtime_log_file_cursors WHERE log_file = \?/, 'PostgreSQL async 游标读取必须访问 dataset schema')
assert.match(repositorySource, /ON CONFLICT\(log_file\) DO UPDATE SET/, 'PostgreSQL async 游标写入必须保持按 log_file upsert 语义')
assert.match(repositorySource, /upsertRuntimeLogFileCursorAsync[\s\S]+positiveInteger\(input\.cursorOffset\)[\s\S]+positiveInteger\(input\.lineNumber\)[\s\S]+positiveInteger\(input\.fileSize\)[\s\S]+integerOrNull\(input\.fileMtimeMs\)[\s\S]+input\.lastReadAt \?\? now/, 'PostgreSQL async 游标写入必须复用 SQLite 参数规范化和 lastReadAt 默认值')
assert.match(queueHealthSource, /protectedRotatedFileCount|pendingFileCount/, '队列健康映射必须传播文件消费指标')
assert.match(statsRoutesSource, /discoveredFileCount|pendingFileCount|protectedRotatedFileCount/, 'stats 运行态必须返回文件消费指标')

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-contract-'))
const { runtimeConfig } = await import('../../config/runtime.js')
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')

const databaseModule = await import('../../storage/database.js')
const repository = await import('../../storage/runtime-logs.repository.js')
const { buildBackgroundQueueHealthSnapshot } = await import('../../modules/background/background-queue-health.service.js')

try {
  await repository.upsertRuntimeLogFileCursorAsync({
    logFile: 'contract.log',
    cursorOffset: -10,
    lineNumber: Number.NaN,
    fileSize: 12.8,
    fileMtimeMs: Number.POSITIVE_INFINITY
  })
  const cursor = await repository.getRuntimeLogFileCursorAsync('contract.log')
  assert.equal(cursor?.cursorOffset, 0, 'async 游标写入必须把负 offset 规范化为 0')
  assert.equal(cursor?.lineNumber, 0, 'async 游标写入必须把非有限行号规范化为 0')
  assert.equal(cursor?.fileSize, 12, 'async 游标写入必须把文件大小规范化为非负整数')
  assert.equal(cursor?.fileMtimeMs, undefined, 'async 游标写入必须把非有限 mtime 规范化为空值')
  assert.match(cursor?.lastReadAt ?? '', /^\d{4}-\d{2}-\d{2}T/, '缺失 lastReadAt 时 async 游标写入必须生成当前时间')

  const fileRuntime = {
    queueLength: 0,
    retentionDays: 14,
    discoveredFileCount: 3,
    pendingFileCount: 2,
    pendingBytes: 4096,
    oldestPendingMtime: '2026-07-20T00:00:00.000Z',
    currentFile: 'juhe-ai.worker.log.1',
    currentOffset: 1024,
    lastReadAt: '2026-07-20T00:01:00.000Z',
    lastCommitAt: '2026-07-20T00:01:01.000Z',
    lastError: 'contract error',
    protectedRotatedFileCount: 1
  }
  const queueHealth = buildBackgroundQueueHealthSnapshot({
    ingestWorker: {
      ready: true,
      snapshot: {
        pid: 1,
        ready: true,
        workerRole: 'ingest-worker',
        jobs: [],
        usageRecordQueue: {},
        auditLogQueue: {},
        operationLogQueue: {},
        publicApiLogQueue: {},
        recordMaintenanceQueue: {},
        runtimeLogIndexQueue: fileRuntime
      }
    }
  } as never)
  const runtimeLogHealth = queueHealth.workerQueues.find((item) => item.key === 'runtimeLogIndex')
  assert.equal(runtimeLogHealth?.pendingFileCount, 2, '运行日志路由底层健康 DTO 必须传播 pendingFileCount')
  assert.equal(runtimeLogHealth?.currentFile, 'juhe-ai.worker.log.1', '运行日志路由底层健康 DTO 必须传播 currentFile')
  assert.equal(runtimeLogHealth?.protectedRotatedFileCount, 1, 'stats 路由底层健康 DTO 必须传播轮转文件保护数量')
  assert.equal(runtimeLogHealth?.status, 'degraded', '运行日志文件消费错误必须进入后台队列健康状态')
  assert.ok(runtimeLogHealth?.reasons.includes('runtime_log_file_error'), '运行日志文件消费错误必须保留可诊断原因')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

const statsRoutes = await import('../../modules/stats/stats.routes.js')
assert.equal(typeof statsRoutes.backgroundQueueHealthRuntimeRow, 'function', 'stats 路由必须导出并调用队列健康 DTO 映射')
const routeRuntime = {
  queueLength: 99,
  retentionDays: 14,
  discoveredFileCount: 4,
  pendingFileCount: 3,
  pendingBytes: 8192,
  currentFile: 'juhe-ai.log.2',
  currentOffset: 2048,
  lastCommitAt: '2026-07-20T01:00:00.000Z',
  protectedRotatedFileCount: 2,
  redisEnqueueTimeoutDropCount: 7
}
const routeQueueHealth = buildBackgroundQueueHealthSnapshot({
  ingestWorker: {
    ready: true,
    snapshot: {
      pid: 2,
      ready: true,
      workerRole: 'ingest-worker',
      jobs: [],
      usageRecordQueue: {},
      auditLogQueue: {},
      operationLogQueue: {},
      publicApiLogQueue: {},
      recordMaintenanceQueue: {},
      runtimeLogIndexQueue: routeRuntime
    }
  }
} as never)
const statsHealthItem = routeQueueHealth.workerQueues.find((item) => item.key === 'runtimeLogIndex')
assert(statsHealthItem, 'stats 路由验证必须构造运行日志健康项')
assert.equal(statsHealthItem.status, 'backlogged', '运行日志文件积压必须进入后台队列健康状态')
assert.ok(statsHealthItem.reasons.includes('runtime_log_file_backlogged'), '运行日志文件积压必须保留可诊断原因')
const statsRouteRow = statsRoutes.backgroundQueueHealthRuntimeRow(statsHealthItem)
assert.equal(statsRouteRow?.localQueue?.pendingFileCount, 3, 'stats 路由 DTO 必须返回文件积压数量')
assert.equal(statsRouteRow?.localQueue?.currentFile, 'juhe-ai.log.2', 'stats 路由 DTO 必须返回当前消费文件')
assert.equal(statsRouteRow?.localQueue?.protectedRotatedFileCount, 2, 'stats 路由 DTO 必须返回轮转文件保护数量')

runtimeConfig.databaseDriver = 'postgres'
let pgQueryCall: { sql: string; params: unknown[] } | undefined
const pgReadClient = {
  query: async (sql: string, params: unknown[]) => {
    pgQueryCall = { sql, params }
    return [{
      log_file: 'pg-contract.log',
      file_identity: 'pg-identity',
      cursor_offset: 32,
      line_number: 4,
      file_size: 64,
      truncation_generation: 3,
      file_mtime_ms: 123,
      last_read_at: '2026-07-20T02:00:00.000Z',
      last_error_message: null,
      created_at: '2026-07-20T01:00:00.000Z',
      updated_at: '2026-07-20T02:00:00.000Z'
    }]
  }
}
const pgCursor = await repository.getRuntimeLogFileCursorAsync('pg-contract.log', {
  getPostgresClient: async () => pgReadClient as never
})
assert.match(pgQueryCall?.sql ?? '', /juhe_dataset\.runtime_log_file_cursors/, 'PG async cursor 读取必须查询 dataset schema')
assert.deepEqual(pgQueryCall?.params, ['pg-contract.log'], 'PG async cursor 读取必须按 logFile 绑定参数')
assert.equal(pgCursor?.cursorOffset, 32, 'PG async cursor 读取必须映射真实查询行')
assert.equal(pgCursor?.truncationGeneration, 3, 'PG async cursor 读取必须映射截断 generation')

let pgExecuteCall: { sql: string; params: unknown[] } | undefined
const pgWriteClient = {
  execute: async (sql: string, params: unknown[]) => {
    pgExecuteCall = { sql, params }
    return { changes: 1 }
  }
}
await repository.upsertRuntimeLogFileCursorAsync({
  logFile: 'pg-contract.log',
  cursorOffset: -2,
  lineNumber: 5.9,
  fileSize: Number.NaN,
  truncationGeneration: -3,
  fileMtimeMs: Number.POSITIVE_INFINITY
}, {
  getPostgresClient: async () => pgWriteClient as never,
  now: () => '2026-07-20T03:00:00.000Z'
})
assert.match(pgExecuteCall?.sql ?? '', /ON CONFLICT\(log_file\) DO UPDATE SET/, 'PG async cursor 写入必须执行 upsert')
assert.deepEqual(pgExecuteCall?.params, [
  'pg-contract.log', null, 0, 5, 0, 0, null,
  '2026-07-20T03:00:00.000Z', null,
  '2026-07-20T03:00:00.000Z', '2026-07-20T03:00:00.000Z'
], 'PG async cursor 写入必须绑定规范化参数和统一默认时间')

const pgReadFailure = new Error('forced pg cursor query failure')
await assert.rejects(
  repository.getRuntimeLogFileCursorAsync('pg-failure.log', {
    getPostgresClient: async () => ({ query: async () => { throw pgReadFailure } }) as never
  }),
  (error) => error === pgReadFailure,
  'PG async cursor 读取必须原样传播 client rejection'
)
const pgWriteFailure = new Error('forced pg cursor execute failure')
await assert.rejects(
  repository.upsertRuntimeLogFileCursorAsync({
    logFile: 'pg-failure.log', cursorOffset: 0, lineNumber: 0, fileSize: 0
  }, {
    getPostgresClient: async () => ({ execute: async () => { throw pgWriteFailure } }) as never
  }),
  (error) => error === pgWriteFailure,
  'PG async cursor 写入必须原样传播 client rejection'
)

console.log('运行日志文件消费领域契约回归通过')
