import { strict as assert } from 'node:assert'
import { DatabaseSync } from 'node:sqlite'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-query-read-only-'))
const runtimeLogPath = join(tempRoot, 'runtime-log.sqlite3')
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.chatDatabasePath = join(tempRoot, 'chat.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.runtimeLogDatabasePath = runtimeLogPath
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context', 'state-shards')
runtimeConfig.processRole = 'server'
runtimeConfig.workerRole = 'worker'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
logger.level = 'silent'

seedRuntimeLogDatabase(runtimeLogPath)

const [databaseModule, queryRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/runtime-log-query.repository.js')
])

try {
  assert.equal(databaseModule.sqliteWriterOwnerForMainDatabase('runtime-log'), 'go-runtime-log', 'F1 SQLite 文件必须只由 Go owner 写入')
  assert.equal(databaseModule.currentProcessOwnsSqliteMainDatabase('runtime-log'), false, '任何 Node 角色都不能成为 F1 SQLite writer')
  assert.equal(databaseModule.mainDatabaseRuntimeInfo('runtime-log').queryOnly, true, 'Node F1 SQLite 连接必须固定只读')

  const runtimeDatabase = databaseModule.getRuntimeLogDatabase()
  assert.throws(
    () => runtimeDatabase.exec('CREATE TABLE forbidden_runtime_log_write(id TEXT PRIMARY KEY)'),
    /attempt to write a readonly database|readonly|query_only|SQLITE_READONLY/i,
    'Node 不得向 Go owner 的 F1 SQLite 文件写入 schema 或数据'
  )

  const page = queryRepository.listRuntimeLogs({ page: 999999, pageSize: 1 })
  assert.equal(page.page, 1000, '运行日志深翻页必须保持固定窗口')
  assert.equal(page.pageSize, 1)
  assert.equal(page.items.length, 0, '超过数据范围的受限窗口必须返回空页')

  const keyword = queryRepository.listRuntimeLogs({ keyword: 'needle', pageSize: 10 })
  assert.deepEqual(keyword.items.map((item) => item.id), ['rt-keyword'], '纯关键词查询只能匹配 message 列')
  assert.equal('rawJson' in keyword.items[0]!, false, '运行日志列表不得携带 rawJson')

  const detail = queryRepository.getRuntimeLogDetail('rt-latest')
  assert.equal(detail?.rawJson, '{"kind":"latest"}', '详情读取必须保留完整 rawJson')
  const delta = queryRepository.getRuntimeLogDetailDeltaReadOnly('rt-latest')
  assert.deepEqual(delta, { id: 'rt-latest', rawJson: '{"kind":"latest"}' }, '详情增量读取必须只返回 id 和 rawJson')

  const facets = queryRepository.getRuntimeLogFacetsReadOnly()
  assert.equal(facets.totalIndexed, 3, '聚合摘要必须来自 Go owner 的专用 SQLite 文件')
  assert.deepEqual(facets.levels, [{ value: 'info', count: 3 }])
  assert.deepEqual(facets.events, ['latest-event', 'keyword-event', 'old-event'])

  const source = readFileSync(resolve('src/storage/runtime-log-query.repository.ts'), 'utf8')
  assert.match(source, /getRuntimeLogDatabase/, 'SQLite 读路径必须使用专用运行日志库')
  assert.doesNotMatch(source, /getDatasetDatabase/, 'F1 查询不得退回 Node dataset 写库')
  for (const method of ['listRuntimeLogsAsync', 'getRuntimeLogDetailAsync', 'getRuntimeLogDetailDeltaAsync', 'getRuntimeLogFacetsAsync']) {
    assert(source.includes(method), `PostgreSQL 读路径必须保留 ${method}`)
  }
  assert.match(source, /juhe_dataset\.runtime_logs/, 'PostgreSQL 查询必须读取 Go owner 的 runtime 表')

  console.log('运行日志只读查询回归通过：SQLite 专库 Node 只读、列表/关键词/详情/聚合及 PostgreSQL 查询契约均已覆盖')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedRuntimeLogDatabase(path: string): void {
  const database = new DatabaseSync(path)
  try {
    const latestTime = new Date().toISOString()
    const keywordTime = new Date(Date.now() - 60_000).toISOString()
    const oldTime = new Date(Date.now() - 120_000).toISOString()
    database.exec(`
CREATE TABLE runtime_logs (
  id TEXT PRIMARY KEY,
  log_file TEXT,
  log_offset INTEGER,
  line_number INTEGER,
  time TEXT NOT NULL,
  level TEXT NOT NULL,
  trace_id TEXT,
  event TEXT,
  message TEXT,
  error_message TEXT,
  raw_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE runtime_log_facet_summary (
  bucket_key TEXT PRIMARY KEY,
  total_count INTEGER NOT NULL,
  earliest_time TEXT,
  latest_time TEXT,
  updated_at TEXT NOT NULL
);
CREATE TABLE runtime_log_level_facets (
  bucket_key TEXT NOT NULL,
  level TEXT NOT NULL,
  count INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (bucket_key, level)
);
CREATE TABLE runtime_log_event_facets (
  bucket_key TEXT NOT NULL,
  event TEXT NOT NULL,
  count INTEGER NOT NULL,
  latest_time TEXT,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (bucket_key, event)
);`)
    const insertLog = database.prepare(`
INSERT INTO runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at)
VALUES (?, 'juhe-ai.log', 0, 1, ?, 'info', 'trace-runtime-log', ?, ?, NULL, ?, ?)`)
    insertLog.run('rt-old', oldTime, 'old-event', 'old message', '{"kind":"old"}', oldTime)
    insertLog.run('rt-keyword', keywordTime, 'keyword-event', 'message needle only', '{"kind":"keyword"}', keywordTime)
    insertLog.run('rt-latest', latestTime, 'latest-event', 'latest message', '{"kind":"latest"}', latestTime)
    database.prepare('INSERT INTO runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at) VALUES (?, ?, ?, ?, ?)')
      .run('current', 3, oldTime, latestTime, latestTime)
    database.prepare('INSERT INTO runtime_log_level_facets (bucket_key, level, count, updated_at) VALUES (?, ?, ?, ?)')
      .run('current', 'info', 3, latestTime)
    const insertEvent = database.prepare('INSERT INTO runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at) VALUES (?, ?, ?, ?, ?)')
    insertEvent.run('current', 'latest-event', 1, latestTime, latestTime)
    insertEvent.run('current', 'keyword-event', 1, keywordTime, keywordTime)
    insertEvent.run('current', 'old-event', 1, oldTime, oldTime)
  } finally {
    database.close()
  }
}
