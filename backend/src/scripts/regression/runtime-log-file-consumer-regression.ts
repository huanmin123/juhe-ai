import { strict as assert } from 'node:assert'
import { mkdirSync, mkdtempSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { assertRuntimeLogFileIndexingConfig, runtimeConfig } from '../../config/runtime.js'

assert.throws(
  () => assertRuntimeLogFileIndexingConfig({ runtimeMode: 'performance', fileEnabled: false }),
  /JUHE_AI_LOG_FILE_ENABLED/,
  'performance 模式关闭文件日志时必须 fail-fast，不能静默失去 runtime_logs 索引'
)
assert.doesNotThrow(() => assertRuntimeLogFileIndexingConfig({ runtimeMode: 'standalone', fileEnabled: false }))

const root = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-consumer-'))
const logDir = join(root, 'logs')
mkdirSync(logDir)
runtimeConfig.log.directory = logDir
runtimeConfig.log.fileEnabled = true
runtimeConfig.log.consoleEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.queueDriver = 'memory'
runtimeConfig.databasePath = join(root, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(root, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(root, 'stats.sqlite3')

const [databaseModule, importerModule, repositoryModule] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/runtime-logs/runtime-log-file-import.service.js'),
  import('../../storage/runtime-logs.repository.js')
])

try {
  const database = databaseModule.getDatasetDatabase()
  const line = (event: string) => JSON.stringify({ time: new Date().toISOString(), level: 30, event, msg: event })
  const currentPath = join(logDir, 'juhe-ai.ingest-worker.log')

  writeFileSync(currentPath, `${line('existing-before-start')}\n`)
  await importerModule.importRuntimeLogFileDeltaForTest({ path: currentPath, role: 'ingest-worker-current' })
  const initialCursor = repositoryModule.getRuntimeLogFileCursor(currentPath)
  assert.equal(initialCursor?.cursorOffset, Buffer.byteLength(`${line('existing-before-start')}\n`), '无游标的当前文件必须从文件尾开始')
  assert.equal((database.prepare('SELECT COUNT(*) AS count FROM runtime_logs').get() as { count: number }).count, 0, '当前文件尾部初始化不得回放历史日志')

  writeFileSync(currentPath, `${line('existing-before-start')}\n${line('after-start')}\n`)
  await importerModule.importRuntimeLogFileDeltaForTest({ path: currentPath, role: 'ingest-worker-current' })
  assert.equal((database.prepare('SELECT COUNT(*) AS count FROM runtime_logs WHERE event = ?').get('after-start') as { count: number }).count, 1, '追加完整行必须被索引')

  writeFileSync(currentPath, `${line('existing-before-start')}\n${line('after-start')}\npartial`)
  await importerModule.importRuntimeLogFileDeltaForTest({ path: currentPath, role: 'ingest-worker-current' })
  const partialCursor = repositoryModule.getRuntimeLogFileCursor(currentPath)
  assert.equal(partialCursor?.cursorOffset, Buffer.byteLength(`${line('existing-before-start')}\n${line('after-start')}\n`), 'partial line 不得推进游标')

  const truncatedLine = `${line('after-same-identity-truncate')}\n`
  writeFileSync(currentPath, truncatedLine)
  await importerModule.importRuntimeLogFileDeltaForTest({ path: currentPath, role: 'ingest-worker-current' })
  assert.equal(
    (database.prepare('SELECT COUNT(*) AS count FROM runtime_logs WHERE event = ?').get('after-same-identity-truncate') as { count: number }).count,
    1,
    '同一 identity 的当前文件原地截断后必须从 offset 0 读取新完整行'
  )
  const truncatedCursor = repositoryModule.getRuntimeLogFileCursor(currentPath)
  assert.equal(truncatedCursor?.cursorOffset, Buffer.byteLength(truncatedLine), '同 identity 截断重读后游标必须落在新文件尾')
  assert.equal(truncatedCursor?.lineNumber, 1, '同 identity 截断后行号必须从 0 重新累计')

  const rotatedPath = join(logDir, 'juhe-ai.ingest-worker.20260721T010203Z.00000000-0000-0000-0000-000000000001.log')
  writeFileSync(rotatedPath, `${line('rotated-first')}\n`)
  await importerModule.importRuntimeLogFileDeltaForTest({ path: rotatedPath, role: 'ingest-worker-rotated' })
  assert.equal((database.prepare('SELECT COUNT(*) AS count FROM runtime_logs WHERE event = ?').get('rotated-first') as { count: number }).count, 1, '无游标轮转文件必须从 offset 0 消费')

  database.prepare(`
    UPDATE runtime_log_file_cursors
    SET cursor_offset = 100, file_size = 1000
    WHERE log_file = ?
  `).run(rotatedPath)
  const laggingCursor = repositoryModule.getRuntimeLogFileCursor(rotatedPath)
  assert.equal(laggingCursor?.cursorOffset, 100, '截断复现必须保留尚未追到旧文件尾的 consumer cursor')
  assert.equal(laggingCursor?.fileSize, 1000, '截断复现必须记录旧 generation 的完整文件大小')

  const nextGenerationLine = `${line('rotated-after-lagging-truncate')}\n`
  const nextGenerationPadding = 500 - Buffer.byteLength(nextGenerationLine)
  assert.ok(nextGenerationPadding > 0, '截断复现行必须能填充到 500 字节')
  writeFileSync(rotatedPath, Buffer.concat([
    Buffer.from(nextGenerationLine),
    Buffer.alloc(nextGenerationPadding, 32)
  ]))
  assert.equal(statSync(rotatedPath).size, 500, '截断复现的新 generation 文件大小必须为 500 字节')
  await importerModule.importRuntimeLogFileDeltaForTest({ path: rotatedPath, role: 'ingest-worker-rotated' })

  const generationRows = database.prepare(`
    SELECT id, event, log_offset
    FROM runtime_logs
    WHERE event IN (?, ?)
    ORDER BY event ASC
  `).all('rotated-first', 'rotated-after-lagging-truncate') as Array<{ id: string; event: string; log_offset: number }>
  assert.equal(generationRows.length, 2, '同一物理文件截断前后相同 offset 的两代日志都必须保留')
  assert.equal(new Set(generationRows.map((row) => row.id)).size, 2, '截断 generation 必须进入 sourceKey，避免相同 offset 的 rtlog ID 冲突')
  assert.ok(generationRows.every((row) => row.log_offset === 0), '截断前后两代日志都必须从 offset 0 建立来源位置')
  const persistedGeneration = database.prepare(`
    SELECT truncation_generation
    FROM runtime_log_file_cursors
    WHERE log_file = ?
  `).get(rotatedPath) as { truncation_generation: number }
  assert.equal(persistedGeneration.truncation_generation, 1, '截断 generation 必须随 cursor 持久化')

  const failurePath = join(logDir, 'juhe-ai.ingest-worker.failure.log')
  writeFileSync(failurePath, `${line('failure-first')}\n${line('failure-second')}\n`)
  const persisted = new Map<string, any>()
  let failBatch = true
  let batchCallCount = 0
  const dependency = importerModule.createRuntimeLogFileImportTestDependencies({
    getCursor: async (path: string) => persisted.get(path),
    getCursorByIdentity: async () => undefined,
    upsertCursor: async (cursor: any) => { persisted.set(cursor.logFile, cursor) },
    createBatch: async () => {
      batchCallCount += 1
      if (failBatch && batchCallCount === 2) throw new Error('forced postgres batch failure')
    },
    batchSize: 1
  })
  runtimeConfig.databaseDriver = 'postgres'
  await importerModule.importRuntimeLogFileDeltaForTest({ path: failurePath, role: 'ingest-worker-rotated' }, dependency)
  assert.equal(persisted.get(failurePath)?.lineNumber, 1, 'PG batch 失败时游标只能停在最近一次成功提交')
  assert.equal(
    persisted.get(failurePath)?.cursorOffset,
    Buffer.byteLength(`${line('failure-first')}\n`),
    'PG batch 失败时不得越过失败批次'
  )
  failBatch = false
  await importerModule.importRuntimeLogFileDeltaForTest({ path: failurePath, role: 'ingest-worker-rotated' }, dependency)
  assert.equal(persisted.get(failurePath)?.lineNumber, 2, '重启/重试必须从最近一次成功游标继续')

  runtimeConfig.databaseDriver = 'sqlite'
  const oldTimestamp = '2000-01-01T00:00:00.000Z'
  database.prepare(`
    UPDATE runtime_log_file_cursors
    SET updated_at = ?, file_size = cursor_offset + 1
    WHERE log_file = ?
  `).run(oldTimestamp, rotatedPath)
  assert.equal(
    repositoryModule.cleanupRuntimeLogFileCursorsBefore('2001-01-01T00:00:00.000Z'),
    0,
    '未完整消费的文件 cursor 不得被 retention cleanup 删除'
  )
  database.prepare(`
    UPDATE runtime_log_file_cursors
    SET file_size = cursor_offset, last_error_message = NULL
    WHERE log_file = ?
  `).run(rotatedPath)
  assert.equal(
    repositoryModule.cleanupRuntimeLogFileCursorsBefore('2001-01-01T00:00:00.000Z'),
    1,
    '完整消费且无错误的过期 cursor 才可进入 retention cleanup'
  )

  console.log('运行日志文件消费者回归通过')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(root, { recursive: true, force: true })
}
