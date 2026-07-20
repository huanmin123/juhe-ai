import { strict as assert } from 'node:assert'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-file-import-source-'))
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

type RuntimeLogSourceRow = {
  id?: string
  log_file: string | null
  log_offset: number | null
  line_number: number | null
}

const [databaseModule, runtimeLogFileImport, runtimeLogsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/runtime-logs/runtime-log-file-import.service.js'),
  import('../../storage/runtime-logs.repository.js')
])

try {
  const database = databaseModule.getDatasetDatabase()
  const now = new Date().toISOString()
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
    msg: '这条超长单行必须完整写入',
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
  const cursorAfterOversized = runtimeLogsRepository.getRuntimeLogFileCursor(logPath)
  assert.equal(cursorAfterOversized?.cursorOffset, expectedNormalOffset, '字节预算落在超长行中间时必须继续读到换行并提交完整行')
  assert.equal(cursorAfterOversized?.lineNumber, 1, '完整写入超长行后行号应推进一行')
  assert.equal(cursorAfterOversized?.lastErrorMessage, undefined, '完整写入超长行不应留下跳过错误')

  const oversizedCount = database
    .prepare('SELECT COUNT(*) AS count, MAX(LENGTH(raw_json)) AS raw_json_length FROM runtime_logs WHERE event = ?')
    .get('oversized_runtime_log_line') as { count: number; raw_json_length: number }
  assert.equal(Number(oversizedCount.count ?? 0), 1, '超长单行必须完整写入索引')
  assert.equal(Number(oversizedCount.raw_json_length ?? 0), longLine.length, '超过 1MB 的 raw_json 不得截断')

  await runtimeLogFileImport.importRuntimeLogFileDeltaForTest({ path: logPath, role: 'db-service-current' })
  const cursorAfterNormal = runtimeLogsRepository.getRuntimeLogFileCursor(logPath)
  assert.equal(cursorAfterNormal?.cursorOffset, statSync(logPath).size, '正常下一行导入后游标应推进到文件末尾')
  assert.equal(cursorAfterNormal?.lineNumber, 2, '正常下一行导入后行号应继续累加')
  assert.equal(cursorAfterNormal?.lastErrorMessage, undefined, '正常恢复导入后应清除上一次超长行错误')

  const normalRows = database
    .prepare(`
      SELECT id, log_file, log_offset, line_number
      FROM runtime_logs
      WHERE event = ?
      ORDER BY log_offset ASC
    `)
    .all('normal_after_oversized_runtime_log_line') as RuntimeLogSourceRow[]
  assert.equal(normalRows.length, 1, '超长单行后的正常日志应在下一轮导入')
  const legacySourceKey = `${cursorAfterNormal?.fileIdentity}:${expectedNormalOffset}`
  const expectedGenerationZeroId = `rtlog_${createHash('sha256').update(legacySourceKey).digest('hex').slice(0, 32)}`
  assert.equal(normalRows[0]?.id, expectedGenerationZeroId, 'generation 0 必须保留旧 identity:offset sourceKey，避免升级重放重复')
  assert.deepEqual(
    [normalRows[0]?.log_file, normalRows[0]?.log_offset, normalRows[0]?.line_number],
    [logPath, expectedNormalOffset, 2],
    '文件导入的正常日志应带准确来源位置'
  )

  writeFileSync(logPath, '')
  await runtimeLogFileImport.importRuntimeLogFileDeltaForTest({ path: logPath, role: 'db-service-current' })
  const unfinishedOversizedLine = 'x'.repeat(3 * 1024 * 1024)
  writeFileSync(logPath, unfinishedOversizedLine)
  await runtimeLogFileImport.importRuntimeLogFileDeltaForTest({ path: logPath, role: 'db-service-current' })
  const cursorAfterUnfinishedOversizedLine = runtimeLogsRepository.getRuntimeLogFileCursor(logPath)
  assert.equal(cursorAfterUnfinishedOversizedLine?.cursorOffset, 0, '未换行超长日志行不应推进主游标')
  assert.equal(cursorAfterUnfinishedOversizedLine?.lastErrorMessage, undefined, '未换行超长日志行等待下轮继续读取时不应伪造跳过错误')

  writeFileSync(logPath, `${longLine}\n${normalLine}\n`)
  await runtimeLogFileImport.importRuntimeLogFileDeltaForTest({ path: logPath, role: 'db-service-current' })
  await runtimeLogFileImport.importRuntimeLogFileDeltaForTest({ path: logPath, role: 'db-service-current' })

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

  console.log('运行日志文件导入来源回归通过：DB service tail、超长单行完整写入和游标恢复均符合预期')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
