import { strict as assert } from 'node:assert'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
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
  import('../../storage/runtime-logs.repository.js')
])

const marker = `pressure_${Date.now()}_${Math.random().toString(16).slice(2)}`
const files = ['server', 'db-service', 'stats-worker'].map((role, index) => ({
  role,
  path: join(logDir, `juhe-ai.${role}.log.20260721T120${index}00Z.pressure-${index}.log`),
  kind: 'rotated' as const
}))
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
  for (let round = 0; round < 40; round += 1) {
    for (const file of files) await importer.importRuntimeLogFileDeltaForTest(file)
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
