import { strict as assert } from 'node:assert'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const root = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-rotation-'))
const logDir = join(root, 'logs')
mkdirSync(logDir)
runtimeConfig.log.directory = logDir
runtimeConfig.log.fileEnabled = true
runtimeConfig.log.consoleEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'

const [loggerModule, importerModule] = await Promise.all([
  import('../../shared/logger.js'),
  import('../../modules/runtime-logs/runtime-log-file-import.service.js')
])

try {
  const workerSource = readFileSync(new URL('../../worker.ts', import.meta.url), 'utf8')
  assert.match(
    workerSource,
    /if \(isIngestWorker\(\)\)[\s\S]+startRuntimeLogFileImport\(\)/,
    'ingest-worker 必须启动 file importer'
  )
  assert.doesNotMatch(
    workerSource,
    /if \(runtimeConfig\.databaseDriver === 'sqlite'\) \{\s*startRuntimeLogFileImport\(\)/,
    'PostgreSQL ingest-worker 不得跳过 file importer'
  )
  const currentPath = join(logDir, 'juhe-ai.stats-worker.log')
  writeFileSync(currentPath, 'one\n')
  const discovered = await importerModule.discoverRuntimeLogFilesForTest()
  assert.ok(discovered.some((file) => file.path === currentPath), '角色当前文件必须被发现')

  const rotatedFileName = loggerModule.rotatedLogFileName(
    'juhe-ai.stats-worker.log',
    new Date('2026-07-21T01:02:03.000Z'),
    '00000000-0000-0000-0000-000000000002'
  )
  const rotatedPath = join(logDir, rotatedFileName)
  writeFileSync(rotatedPath, 'two\n')
  const discoveredWithRotation = await importerModule.discoverRuntimeLogFilesForTest()
  assert.ok(discoveredWithRotation.some((file) => file.path === rotatedPath), '角色 basename 必须保留并被轮转发现')
  assert.match(rotatedPath, /juhe-ai\.stats-worker\./, '轮转文件名必须包含角色 basename')

  const result = await loggerModule.cleanupRotatedLogFilesForTest({
    directory: logDir,
    maxFiles: 0,
    retentionDays: 0,
    canDeleteRotatedFile: async () => false
  })
  assert.equal(result.deletedFileCount, 0, '未消费轮转文件必须受到清理保护')

  console.log('运行日志轮转与清理回归通过')
} finally {
  rmSync(root, { recursive: true, force: true })
}
