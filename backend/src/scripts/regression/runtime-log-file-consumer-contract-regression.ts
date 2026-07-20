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
const runtimeRoutesSource = source('../../modules/runtime-logs/runtime-logs.routes.ts')
const frontendTypesSource = source('../../../../frontend/src/types/domain/runtime-logs.ts')
const frontendFacetsSource = source('../../../../frontend/src/views/runtime-logs/runtimeLogFacets.ts')

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
  assert.match(frontendTypesSource, new RegExp(`\\b${field}\\b`), `前端运行日志契约必须暴露 ${field}`)
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
assert.match(repositorySource, /runtimeConfig\.databaseDriver !== 'postgres'[\s\S]+getRuntimeLogFileCursor\(logFile\)/, 'SQLite async 游标读取必须复用同步语义')
assert.match(repositorySource, /SELECT \* FROM juhe_dataset\.runtime_log_file_cursors WHERE log_file = \?/, 'PostgreSQL async 游标读取必须访问 dataset schema')
assert.match(repositorySource, /ON CONFLICT\(log_file\) DO UPDATE SET/, 'PostgreSQL async 游标写入必须保持按 log_file upsert 语义')
assert.match(repositorySource, /upsertRuntimeLogFileCursorAsync[\s\S]+positiveInteger\(input\.cursorOffset\)[\s\S]+positiveInteger\(input\.lineNumber\)[\s\S]+positiveInteger\(input\.fileSize\)[\s\S]+integerOrNull\(input\.fileMtimeMs\)[\s\S]+input\.lastReadAt \?\? now/, 'PostgreSQL async 游标写入必须复用 SQLite 参数规范化和 lastReadAt 默认值')
assert.match(queueHealthSource, /protectedRotatedFileCount|pendingFileCount/, '队列健康映射必须传播文件消费指标')
assert.match(statsRoutesSource, /discoveredFileCount|pendingFileCount|protectedRotatedFileCount/, 'stats 运行态必须返回文件消费指标')
assert.match(runtimeRoutesSource, /runtime:\s*runtimeLogIndexQueue/, '运行日志 facets 路由必须返回文件消费运行态')
assert.match(frontendFacetsSource, /facets\.runtime\?\.lastError/, '前端 facets 映射必须识别文件消费错误状态')

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
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('运行日志文件消费领域契约回归通过')
