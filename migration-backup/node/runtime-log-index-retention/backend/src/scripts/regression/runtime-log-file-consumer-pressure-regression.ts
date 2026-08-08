import { strict as assert } from 'node:assert'
import { mkdirSync, mkdtempSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-pressure-'))
const logDir = join(tempRoot, 'logs')
mkdirSync(logDir)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.log.directory = logDir
runtimeConfig.log.fileEnabled = true
runtimeConfig.log.consoleEnabled = false

const [databaseModule, importer, repository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/runtime-logs/runtime-log-file-import.service.js'),
  import('../../storage/runtime-log-index.repository.js')
])

const marker = `pressure_${Date.now()}_${Math.random().toString(16).slice(2)}`
const files = [
  { role: 'server', path: join(logDir, 'juhe-ai.20260721T120000Z.00000000-0000-0000-0000-000000000101.log'), kind: 'rotated' as const },
  { role: 'db-service', path: join(logDir, 'juhe-ai.db-service.20260721T120100Z.00000000-0000-0000-0000-000000000102.log'), kind: 'rotated' as const },
  { role: 'stats-worker', path: join(logDir, 'juhe-ai.stats-worker.20260721T120200Z.00000000-0000-0000-0000-000000000103.log'), kind: 'rotated' as const }
]
const linesPerFile = 2_000
const expectedEvents: string[] = []

try {
  const database = databaseModule.getDatasetDatabase()
  for (const file of files) {
    const lines = Array.from({ length: linesPerFile }, (_, index) => {
      const event = `${marker}_${file.role}_${index}`
      expectedEvents.push(event)
      return `${JSON.stringify({ time: new Date().toISOString(), level: 30, event, msg: `pressure ${index} ${'x'.repeat(640)}` })}\n`
    }).join('')
    writeFileSync(file.path, lines)
  }

  let imported = 0
  await importer.resetRuntimeLogFileDiscoveryForTest()
  for (let round = 0; round < 40; round += 1) {
    const discovered = await importer.discoverRuntimeLogFilesForTest()
    for (const file of discovered.filter((item) => files.some((expected) => expected.path === item.path))) {
      await importer.importRuntimeLogFileDeltaForTest(file)
    }
    imported = Number((database.prepare('SELECT COUNT(*) AS count FROM runtime_logs WHERE event LIKE ?').get(`${marker}%`) as { count?: number }).count ?? 0)
    if (imported === expectedEvents.length) break
  }

  assert.equal(imported, expectedEvents.length, '暂停消费期间产生的完整日志 backlog 必须全部追平')
  const duplicateCount = Number((database.prepare('SELECT COUNT(*) AS count, COUNT(DISTINCT id) AS distinct_count FROM runtime_logs WHERE event LIKE ?').get(`${marker}%`) as { count?: number; distinct_count?: number }).count ?? 0)
  const distinctCount = Number((database.prepare('SELECT COUNT(DISTINCT id) AS count FROM runtime_logs WHERE event LIKE ?').get(`${marker}%`) as { count?: number }).count ?? 0)
  assert.equal(duplicateCount, distinctCount, '压力消费不能产生重复稳定 ID')
  for (const file of files) {
    const cursor = repository.getRuntimeLogFileCursor(file.path)
    assert.equal(cursor?.cursorOffset, statSync(file.path).size, `${file.role} cursor 必须追平文件尾部`)
  }
  console.log(`运行日志文件消费压力回归通过：files=${files.length} lines=${expectedEvents.length} imported=${imported}`)
} finally {
  try {
    const database = databaseModule.getDatasetDatabase()
    database.prepare('DELETE FROM runtime_logs WHERE event LIKE ?').run(`${marker}%`)
    for (const file of files) database.prepare('DELETE FROM runtime_log_file_cursors WHERE log_file = ?').run(file.path)
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
