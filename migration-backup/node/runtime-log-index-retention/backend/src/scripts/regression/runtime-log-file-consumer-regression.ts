import { strict as assert } from 'node:assert'
import { mkdirSync, mkdtempSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs'
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
  import('../../storage/runtime-log-index.repository.js')
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

  const replacedCurrentPath = join(logDir, 'juhe-ai.ingest-worker.20260721T010202Z.00000000-0000-0000-0000-000000000000.log')
  renameSync(currentPath, replacedCurrentPath)
  const replacementContent = `${line('after-current-replacement')}\n${line('after-current-replacement-padding')}\n`
  writeFileSync(currentPath, replacementContent)
  await importerModule.importRuntimeLogFileDeltaForTest({ path: currentPath, role: 'ingest-worker-current', kind: 'current' })
  assert.equal(
    (database.prepare('SELECT COUNT(*) AS count FROM runtime_logs WHERE event = ?').get('after-current-replacement') as { count: number }).count,
    1,
    '轮转后新 current identity 必须从 offset 0 消费，不能把首批日志当作启动前历史跳过'
  )

  writeFileSync(currentPath, `${replacementContent}partial`)
  await importerModule.importRuntimeLogFileDeltaForTest({ path: currentPath, role: 'ingest-worker-current' })
  const partialCursor = repositoryModule.getRuntimeLogFileCursor(currentPath)
  assert.equal(partialCursor?.cursorOffset, Buffer.byteLength(replacementContent), 'partial line 不得推进游标')

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

  const completedPath = join(logDir, 'juhe-ai.ingest-worker.20260721T010204Z.00000000-0000-0000-0000-000000000002.log')
  writeFileSync(completedPath, `${line('completed-cache')}\n`)
  const completedCursors = new Map<string, any>()
  let completedCursorReadCount = 0
  let completedCursorWriteCount = 0
  let completedNowMs = 1_000
  const completedDependency = importerModule.createRuntimeLogFileImportTestDependencies({
    getCursor: async (path: string) => {
      completedCursorReadCount += 1
      return completedCursors.get(path)
    },
    getCursorByIdentity: async () => undefined,
    upsertCursor: async (cursor: any) => {
      completedCursorWriteCount += 1
      completedCursors.set(cursor.logFile, cursor)
    },
    createBatch: async () => undefined,
    nowMs: () => completedNowMs
  })
  await importerModule.importRuntimeLogFileDeltaForTest({ path: completedPath, role: 'ingest-worker-rotated', kind: 'rotated' }, completedDependency)
  const readsAfterCompletion = completedCursorReadCount
  const writesAfterCompletion = completedCursorWriteCount
  await importerModule.importRuntimeLogFileDeltaForTest({ path: completedPath, role: 'ingest-worker-rotated', kind: 'rotated' }, completedDependency)
  assert.equal(completedCursorReadCount, readsAfterCompletion, '未变化且已追平的文件在缓存续租期内不得重复读取 PostgreSQL cursor')
  assert.equal(completedCursorWriteCount, writesAfterCompletion, '未变化且已追平的文件在缓存续租期内不得重复写 PostgreSQL cursor')
  completedNowMs += 60 * 60 * 1000
  await importerModule.importRuntimeLogFileDeltaForTest({ path: completedPath, role: 'ingest-worker-rotated', kind: 'rotated' }, completedDependency)
  assert.equal(completedCursorReadCount, readsAfterCompletion + 1, '完成缓存到期后必须重新读取 PostgreSQL cursor')
  assert.equal(completedCursorWriteCount, writesAfterCompletion + 1, '完成缓存到期后必须续租 PostgreSQL cursor，避免 retention 误删')
  const readsBeforeRestart = completedCursorReadCount
  const writesBeforeRestart = completedCursorWriteCount
  await importerModule.resetRuntimeLogFileDiscoveryForTest()
  await importerModule.importRuntimeLogFileDeltaForTest({ path: completedPath, role: 'ingest-worker-rotated', kind: 'rotated' }, completedDependency)
  assert.equal(completedCursorReadCount, readsBeforeRestart + 1, '重启后首次观察已追平文件必须重新读取 PostgreSQL cursor')
  assert.equal(completedCursorWriteCount, writesBeforeRestart + 1, '重启后首次观察已追平文件必须立即续租 cursor，早于 retention cleanup')

  const relocatedPath = join(logDir, 'juhe-ai.ingest-worker.20260721T010205Z.00000000-0000-0000-0000-000000000003.log')
  const relocatedContent = `${line('identity-relocation')}\n`
  writeFileSync(relocatedPath, relocatedContent)
  const relocatedWrites: any[] = []
  let relocatedBatchCount = 0
  const relocatedDependency = importerModule.createRuntimeLogFileImportTestDependencies({
    getCursor: async () => undefined,
    getCursorByIdentity: async () => ({
      logFile: currentPath,
      fileIdentity: 'old-path-identity',
      cursorOffset: Buffer.byteLength(relocatedContent),
      lineNumber: 7,
      fileSize: Buffer.byteLength(relocatedContent),
      truncationGeneration: 2,
      fileMtimeMs: Math.trunc(statSync(relocatedPath).mtimeMs),
      lastReadAt: new Date().toISOString(),
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString()
    }),
    upsertCursor: async (cursor: any) => { relocatedWrites.push(cursor) },
    createBatch: async () => { relocatedBatchCount += 1 }
  })
  await importerModule.importRuntimeLogFileDeltaForTest({ path: relocatedPath, role: 'ingest-worker-rotated', kind: 'rotated' }, relocatedDependency)
  assert.equal(relocatedBatchCount, 0, '已追平 current cursor 按 identity 迁移到 rotated 路径时不得从头重读文件')
  assert.ok(relocatedWrites.some((cursor) => cursor.logFile === relocatedPath && cursor.truncationGeneration === 2), 'identity cursor 迁移到 rotated 路径时必须立即持久化并保留 generation')

  const delayedCurrentPath = join(logDir, 'juhe-ai.stats-worker.log')
  const delayedRotatedPath = join(logDir, 'juhe-ai.stats-worker.20260721T010206Z.00000000-0000-0000-0000-000000000004.log')
  const delayedOldContent = `${line('delayed-rotated-old')}\n`
  writeFileSync(delayedCurrentPath, delayedOldContent)
  const delayedCursors = new Map<string, any>()
  let delayedBatchCount = 0
  const delayedDependency = importerModule.createRuntimeLogFileImportTestDependencies({
    getCursor: async (path: string) => delayedCursors.get(path),
    getCursorByIdentity: async (identity: string) => [...delayedCursors.values()].find((cursor) => cursor.fileIdentity === identity),
    upsertCursor: async (cursor: any) => { delayedCursors.set(cursor.logFile, cursor) },
    createBatch: async () => { delayedBatchCount += 1 }
  })
  await importerModule.importRuntimeLogFileDeltaForTest({ path: delayedCurrentPath, role: 'stats-worker-current', kind: 'current' }, delayedDependency)
  delayedCursors.set(delayedCurrentPath, { ...delayedCursors.get(delayedCurrentPath), truncationGeneration: 3 })
  renameSync(delayedCurrentPath, delayedRotatedPath)
  writeFileSync(delayedCurrentPath, `${line('delayed-current-new')}\n`)
  await importerModule.importRuntimeLogFileDeltaForTest({ path: delayedCurrentPath, role: 'stats-worker-current', kind: 'current' }, delayedDependency)
  const preservedOldCursor = [...delayedCursors.values()].find((cursor) => cursor.logFile.startsWith('__runtime_log_identity__:'))
  assert.equal(preservedOldCursor?.truncationGeneration, 3, 'current 先于 rotated 被发现时必须先按 identity 持久化旧 generation')
  const batchCountBeforeDelayedRotated = delayedBatchCount
  await importerModule.importRuntimeLogFileDeltaForTest({ path: delayedRotatedPath, role: 'stats-worker-rotated', kind: 'rotated' }, delayedDependency)
  assert.equal(delayedBatchCount, batchCountBeforeDelayedRotated, '跨目录发现窗口延迟出现的 rotated 文件不得从 offset 0 重读')
  assert.equal(delayedCursors.get(delayedRotatedPath)?.truncationGeneration, 3, '延迟出现的 rotated 文件必须继承 current 被覆盖前的 generation')

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
