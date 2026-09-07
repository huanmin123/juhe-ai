import { strict as assert } from 'node:assert'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

process.env.JUHE_AI_LOG_FILE_ENABLED = 'false'
process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'

const { parseRuntimeLogFileName } = await import('../../shared/runtime-log-file-name.js')
const { cleanupRotatedLogFilesForTest, isLogMaintenanceOwner } = await import('../../shared/logger.js')

const fileNameCases = [
  ['juhe-ai.log', 'server', 'current'],
  ['juhe-ai.usage-worker.log', 'usage-worker', 'current'],
  ['juhe-ai.log-worker.log', 'log-worker', 'current'],
  ['juhe-ai.node-a.log', 'server:node-a', 'current'],
  ['juhe-ai.usage-worker.node-a.log', 'usage-worker:node-a', 'current'],
  ['juhe-ai.log-worker.node-b.log', 'log-worker:node-b', 'current'],
  ['juhe-ai.usage-worker.node-a.20260726T010203Z.00000000-0000-0000-0000-000000000001.log', 'usage-worker:node-a', 'rotated'],
  ['juhe-ai.log-worker.node-b.20260726T010203Z.00000000-0000-0000-0000-000000000002.log', 'log-worker:node-b', 'rotated'],
  ['juhe-ai.node-a.20260726T010203Z.00000000-0000-0000-0000-000000000003.log', 'server:node-a', 'rotated']
] as const

for (const [fileName, role, kind] of fileNameCases) {
  const parsed = parseRuntimeLogFileName(fileName)
  assert.equal(parsed?.role, role, `${fileName} 应解析为角色 ${role}`)
  assert.equal(parsed?.kind, kind, `${fileName} 应解析为 ${kind}`)
}
assert.equal(parseRuntimeLogFileName('juhe-ai..bad.log'), undefined)
assert.equal(parseRuntimeLogFileName('application.log'), undefined)

assert.equal(isLogMaintenanceOwner({ runtimeMode: 'standalone', processRole: 'worker', workerRole: 'ingest-worker', workerReplicaIndex: 0 }), true)
assert.equal(isLogMaintenanceOwner({ runtimeMode: 'standalone', processRole: 'worker', workerRole: 'stats-worker', workerReplicaIndex: 0 }), false)
assert.equal(isLogMaintenanceOwner({ runtimeMode: 'performance', processRole: 'worker', workerRole: 'log-worker', workerReplicaIndex: 0 }), true)
assert.equal(isLogMaintenanceOwner({ runtimeMode: 'performance', processRole: 'worker', workerRole: 'log-worker', workerReplicaIndex: 1 }), false)
assert.equal(isLogMaintenanceOwner({ runtimeMode: 'performance', processRole: 'worker', workerRole: 'usage-worker', workerReplicaIndex: 0 }), false)
assert.equal(isLogMaintenanceOwner({ runtimeMode: 'performance', processRole: 'server', workerRole: 'worker', workerReplicaIndex: 0 }), false)

const root = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-name-'))
try {
  const logDirectory = join(root, 'logs')
  mkdirSync(logDirectory)
  writeFileSync(join(logDirectory, 'juhe-ai.usage-worker.node-a.log'), 'current usage\n')
  writeFileSync(join(logDirectory, 'juhe-ai.log-worker.node-b.log'), 'current log\n')
  writeFileSync(join(logDirectory, fileNameCases[6][0]), 'rotated usage\n')
  writeFileSync(join(logDirectory, fileNameCases[7][0]), 'rotated log\n')

  const cleanup = await cleanupRotatedLogFilesForTest({
    directory: logDirectory,
    maxFiles: 2,
    retentionDays: 30,
    canDeleteRotatedFile: async () => true
  })
  assert.equal(cleanup.currentFileCount, 2, '带 instanceId 的 current 文件必须计入全局保留上限')
  assert.equal(cleanup.deletedFileCount, 2, '带 instanceId 的 usage/log worker 轮转文件必须进入清理候选')
  assert.equal(cleanup.retainedRotatedFileCount, 0)
} finally {
  rmSync(root, { recursive: true, force: true })
}

console.log('运行日志文件名共享解析与单 owner 判定回归通过')
