import { strict as assert } from 'node:assert'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-keyword-only-sql-'))
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'

const [databaseModule, runtimeLogIndexRepository, runtimeLogQueryRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/runtime-log-index.repository.js'),
  import('../../storage/runtime-log-query.repository.js')
])
const runtimeLogsRepository = { ...runtimeLogIndexRepository, ...runtimeLogQueryRepository }

try {
  const now = new Date().toISOString()
  runtimeLogsRepository.createRuntimeLogsBatch([
    {
      id: 'rtlog_keyword_only_match',
      time: now,
      level: 'info',
      event: 'keyword_only_match_event',
      message: '前缀 keywordonlyneedle 后缀',
      rawJson: JSON.stringify({
        time: now,
        level: 'info',
        event: 'keyword_only_match_event',
        msg: 'keywordonlyneedle'
      }),
      createdAt: now
    },
    {
      id: 'rtlog_keyword_only_miss',
      time: now,
      level: 'info',
      event: 'keyword_only_miss_event',
      message: 'unrelated message',
      rawJson: JSON.stringify({
        time: now,
        level: 'info',
        event: 'keywordonlyneedle',
        msg: 'unrelated message',
        body: 'keywordonlyneedle'
      }),
      createdAt: now
    }
  ])

  const keywordOnly = runtimeLogsRepository.listRuntimeLogs({ keyword: 'keywordonlyneedle', pageSize: 10 })
  assert.equal(keywordOnly.items.length, 1, '只有 keyword、没有其他筛选条件时应生成合法 message 模糊查询')
  assert.equal(keywordOnly.items[0]?.event, 'keyword_only_match_event')

  const keywordWithFilter = runtimeLogsRepository.listRuntimeLogs({
    keyword: 'keywordonlyneedle',
    event: 'keyword_only_match_event',
    pageSize: 10
  })
  assert.equal(keywordWithFilter.items.length, 1, 'keyword 与普通筛选条件组合时仍应保留 AND 语义')
  assert.equal(keywordWithFilter.items[0]?.id, 'rtlog_keyword_only_match')

  console.log('运行日志纯 keyword SQL 回归通过：无其他 filter 时只在 message 列模糊匹配')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
