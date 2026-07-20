import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

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
assert.match(queueHealthSource, /protectedRotatedFileCount|pendingFileCount/, '队列健康映射必须传播文件消费指标')
assert.match(statsRoutesSource, /discoveredFileCount|pendingFileCount|protectedRotatedFileCount/, 'stats 运行态必须返回文件消费指标')
assert.match(runtimeRoutesSource, /runtime:\s*runtimeLogIndexQueue/, '运行日志 facets 路由必须返回文件消费运行态')
assert.match(frontendFacetsSource, /facets\.runtime\?\.lastError/, '前端 facets 映射必须识别文件消费错误状态')

console.log('运行日志文件消费领域契约回归通过')
