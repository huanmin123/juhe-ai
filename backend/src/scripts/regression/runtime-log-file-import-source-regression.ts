import { strict as assert } from 'node:assert'
import { mkdirSync, mkdtempSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-file-import-source-'))
const logDir = join(tempRoot, 'logs')
mkdirSync(logDir)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.processRole = 'worker'
runtimeConfig.log.directory = logDir
runtimeConfig.log.fileEnabled = true
runtimeConfig.log.consoleEnabled = false

type RuntimeLogSourceRow = {
  log_file: string | null
  log_offset: number | null
  line_number: number | null
}

const [databaseModule, runtimeLogIndexQueue, runtimeLogFileImport, runtimeLogsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/runtime-logs/runtime-log-index-queue.service.js'),
  import('../../modules/runtime-logs/runtime-log-file-import.service.js'),
  import('../../storage/runtime-logs.repository.js')
])

try {
  const database = databaseModule.getRecordDatabase()
  const now = new Date().toISOString()
  const repeatedLine = JSON.stringify({
    time: now,
    level: 30,
    event: 'duplicate_runtime_log_source_event',
    msg: '相同内容的两条运行日志'
  })

  runtimeLogIndexQueue.enqueueRuntimeLogLineLocal(repeatedLine, {
    sourceKey: 'test-source-a:0',
    logFile: 'source-a.log',
    logOffset: 0,
    lineNumber: 1
  })
  runtimeLogIndexQueue.enqueueRuntimeLogLineLocal(repeatedLine, {
    sourceKey: 'test-source-b:10',
    logFile: 'source-b.log',
    logOffset: 10,
    lineNumber: 2
  })
  runtimeLogIndexQueue.flushAllRuntimeLogIndexQueue()

  const duplicateRows = database
    .prepare(`
      SELECT log_file, log_offset, line_number
      FROM runtime_logs
      WHERE event = ?
      ORDER BY log_file ASC
    `)
    .all('duplicate_runtime_log_source_event') as RuntimeLogSourceRow[]
  assert.equal(duplicateRows.length, 2, '相同原始日志只要来源键不同就都应写入索引')
  assert.deepEqual(
    duplicateRows.map((row) => [row.log_file, row.log_offset, row.line_number]),
    [
      ['source-a.log', 0, 1],
      ['source-b.log', 10, 2]
    ],
    '运行日志索引应保留来源文件、offset 和行号'
  )

  const dedupeLine = JSON.stringify({
    time: now,
    level: 30,
    event: 'same_runtime_log_source_event',
    msg: '同一来源的 live 与 file tail 不应重复'
  })
  const dedupeSourceKey = 'same-source.log:0'
  runtimeLogIndexQueue.enqueueRuntimeLogLineLocal(dedupeLine, {
    sourceKey: dedupeSourceKey,
    logFile: 'same-source.log',
    logOffset: 0,
    lineNumber: 1
  })
  runtimeLogIndexQueue.enqueueRuntimeLogLineLocal(dedupeLine, {
    sourceKey: dedupeSourceKey,
    logFile: 'same-source.log',
    logOffset: 0,
    lineNumber: 1
  })
  runtimeLogIndexQueue.flushAllRuntimeLogIndexQueue()
  const sameSourceCount = database
    .prepare('SELECT COUNT(*) AS count FROM runtime_logs WHERE event = ?')
    .get('same_runtime_log_source_event') as { count: number }
  assert.equal(Number(sameSourceCount.count ?? 0), 1, 'live 与 file tail 传入同一来源键时应保持幂等')

  const activeFileNames = runtimeLogFileImport.activeRuntimeLogFilesForTest()
    .map((file) => basename(file.path))
  assert(activeFileNames.includes('juhe-ai.db-service.log'), '运行日志文件导入应覆盖 DB service 当前日志')

  const logPath = join(logDir, 'juhe-ai.db-service.log')
  writeFileSync(logPath, '')
  await runtimeLogFileImport.importRuntimeLogFileDeltaForTest({ path: logPath, role: 'db-service-current' })

  const longLine = JSON.stringify({
    time: now,
    level: 30,
    event: 'oversized_runtime_log_line',
    msg: '这条超长单行应被跳过',
    body: 'x'.repeat(1024 * 1024 + 128)
  })
  const normalLine = JSON.stringify({
    time: now,
    level: 30,
    event: 'normal_after_oversized_runtime_log_line',
    msg: '超长单行后的正常日志'
  })
  writeFileSync(logPath, `${longLine}\n${normalLine}\n`)

  await runtimeLogFileImport.importRuntimeLogFileDeltaForTest({ path: logPath, role: 'db-service-current' })
  const expectedNormalOffset = Buffer.byteLength(`${longLine}\n`, 'utf8')
  const cursorAfterSkip = runtimeLogsRepository.getRuntimeLogFileCursor(logPath)
  assert.equal(cursorAfterSkip?.cursorOffset, expectedNormalOffset, '超长单行只应在找到完整换行后推进到下一行开头')
  assert.equal(cursorAfterSkip?.lineNumber, 1, '跳过完整超长单行后行号应推进一行')
  assert.match(cursorAfterSkip?.lastErrorMessage ?? '', /超长行/, '跳过超长单行应留下可排查的游标错误信息')

  const oversizedCount = database
    .prepare('SELECT COUNT(*) AS count FROM runtime_logs WHERE event = ?')
    .get('oversized_runtime_log_line') as { count: number }
  assert.equal(Number(oversizedCount.count ?? 0), 0, '超长单行被跳过时不应把半行写入索引')

  await runtimeLogFileImport.importRuntimeLogFileDeltaForTest({ path: logPath, role: 'db-service-current' })
  const cursorAfterNormal = runtimeLogsRepository.getRuntimeLogFileCursor(logPath)
  assert.equal(cursorAfterNormal?.cursorOffset, statSync(logPath).size, '正常下一行导入后游标应推进到文件末尾')
  assert.equal(cursorAfterNormal?.lineNumber, 2, '正常下一行导入后行号应继续累加')
  assert.equal(cursorAfterNormal?.lastErrorMessage, undefined, '正常恢复导入后应清除上一次超长行错误')

  const normalRows = database
    .prepare(`
      SELECT log_file, log_offset, line_number
      FROM runtime_logs
      WHERE event = ?
      ORDER BY log_offset ASC
    `)
    .all('normal_after_oversized_runtime_log_line') as RuntimeLogSourceRow[]
  assert.equal(normalRows.length, 1, '超长单行后的正常日志应在下一轮导入')
  assert.deepEqual(
    [normalRows[0]?.log_file, normalRows[0]?.log_offset, normalRows[0]?.line_number],
    [logPath, expectedNormalOffset, 2],
    '文件导入的正常日志应带准确来源位置'
  )

  const flushFailureLine = JSON.stringify({
    time: now,
    level: 30,
    event: 'runtime_log_cursor_flush_failure',
    msg: '索引写入失败时不能推进文件游标'
  })
  writeFileSync(logPath, `${longLine}\n${normalLine}\n${flushFailureLine}\n`)
  database.exec(`
    CREATE TRIGGER runtime_log_cursor_flush_failure_guard
    BEFORE INSERT ON runtime_logs
    WHEN NEW.event = 'runtime_log_cursor_flush_failure'
    BEGIN
      SELECT RAISE(ABORT, 'forced runtime log flush failure');
    END;
  `)
  await runtimeLogFileImport.importRuntimeLogFileDeltaForTest({ path: logPath, role: 'db-service-current' })
  const cursorAfterFlushFailure = runtimeLogsRepository.getRuntimeLogFileCursor(logPath)
  assert.equal(cursorAfterFlushFailure?.cursorOffset, statSync(logPath).size - Buffer.byteLength(`${flushFailureLine}\n`, 'utf8'), '运行日志索引写入失败时文件游标不应越过未落库行')
  assert.equal(cursorAfterFlushFailure?.lineNumber, 2, '运行日志索引写入失败时行号应保留在最近一次成功写入位置')
  assert.match(cursorAfterFlushFailure?.lastErrorMessage ?? '', /索引写入失败/, '运行日志索引写入失败应在文件游标留下错误原因')
  const failedFlushCount = database
    .prepare('SELECT COUNT(*) AS count FROM runtime_logs WHERE event = ?')
    .get('runtime_log_cursor_flush_failure') as { count: number }
  assert.equal(Number(failedFlushCount.count ?? 0), 0, '运行日志索引写入失败时不应写入失败行')

  database.exec('DROP TRIGGER runtime_log_cursor_flush_failure_guard')
  await runtimeLogFileImport.importRuntimeLogFileDeltaForTest({ path: logPath, role: 'db-service-current' })
  const cursorAfterFlushRetry = runtimeLogsRepository.getRuntimeLogFileCursor(logPath)
  assert.equal(cursorAfterFlushRetry?.cursorOffset, statSync(logPath).size, '运行日志索引恢复后文件游标应推进到文件末尾')
  assert.equal(cursorAfterFlushRetry?.lineNumber, 3, '运行日志索引恢复后行号应继续推进')
  assert.equal(cursorAfterFlushRetry?.lastErrorMessage, undefined, '运行日志索引恢复后应清除失败游标错误')
  const recoveredFlushCount = database
    .prepare('SELECT COUNT(*) AS count FROM runtime_logs WHERE event = ?')
    .get('runtime_log_cursor_flush_failure') as { count: number }
  assert.equal(Number(recoveredFlushCount.count ?? 0), 1, '运行日志索引恢复后应重读并写入之前失败的日志行且保持幂等')

  console.log('运行日志文件导入来源回归通过：重复来源、DB service tail 和超长单行游标均符合预期')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
