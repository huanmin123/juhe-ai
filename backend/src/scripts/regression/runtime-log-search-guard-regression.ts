import { strict as assert } from 'node:assert'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-search-guard-'))
const logDir = join(tempRoot, 'logs')
mkdirSync(logDir)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.processRole = 'worker'
runtimeConfig.log.directory = logDir
runtimeConfig.log.fileEnabled = true
runtimeConfig.log.consoleEnabled = false

const [databaseModule, runtimeLogsRepository, runtimeLogGrep] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/runtime-logs.repository.js'),
  import('../../modules/runtime-logs/runtime-log-grep.service.js')
])

try {
  const now = new Date().toISOString()
  runtimeLogsRepository.createRuntimeLogsBatch([
    {
      id: 'rtlog_short_keyword_guard',
      time: now,
      level: 'info',
      event: 'short_keyword_guard_event',
      message: 'e',
      rawJson: JSON.stringify({
        time: now,
        level: 'info',
        event: 'short_keyword_guard_event',
        msg: 'e',
        body: 'x'.repeat(150 * 1024)
      }),
      createdAt: now
    },
    {
      id: 'rtlog_long_keyword_guard',
      time: now,
      level: 'info',
      event: 'long_keyword_guard_event',
      message: 'guardneedle',
      rawJson: JSON.stringify({
        time: now,
        level: 'info',
        event: 'long_keyword_guard_event',
        msg: 'guardneedle'
      }),
      createdAt: now
    }
  ])

  const shortKeyword = runtimeLogsRepository.listRuntimeLogs({ keyword: 'e', pageSize: 10 })
  assert.equal(shortKeyword.items.length, 0, '索引查询不应为短关键词退回 raw_json LIKE 扫描')

  const longKeyword = runtimeLogsRepository.listRuntimeLogs({ keyword: 'guardneedle', pageSize: 10 })
  assert.equal(longKeyword.items.length, 1, '足够长的关键词仍应走 FTS 搜索')
  assert.equal(longKeyword.items[0]?.event, 'long_keyword_guard_event')

  const largeDetail = runtimeLogsRepository.getRuntimeLogDetail('rtlog_short_keyword_guard')
  assert(largeDetail?.rawJson.includes('[truncated]'), '仓储层直接写入大 rawJson 也应兜底截断')
  assert((largeDetail?.rawJson.length ?? 0) < 140 * 1024, '仓储层兜底截断应限制 rawJson 入库尺寸')

  writeFileSync(join(logDir, 'juhe-ai.log'), [
    JSON.stringify({
      time: now,
      level: 30,
      event: 'short_grep_keyword_event',
      msg: 'e'
    }),
    JSON.stringify({
      time: now,
      level: 30,
      event: 'long_grep_keyword_event',
      msg: 'grepneedle'
    }),
    JSON.stringify({
      time: now,
      level: 30,
      event: 'large_grep_keyword_event',
      msg: 'grepneedle',
      body: 'x'.repeat(24_000)
    })
  ].join('\n') + '\n')

  const shortGrep = await runtimeLogGrep.grepRuntimeLogFiles({ keywords: ['e'], limit: 10 })
  assert.equal(shortGrep.available, true, '短 grep 关键词应返回可用但空结果')
  assert.equal(shortGrep.items.length, 0, 'grep 不应执行少于 3 字符的短关键词搜索')
  assert.match(shortGrep.message ?? '', /至少需要 3 个字符/, '短 grep 关键词应给出中文提示')

  const longGrep = await runtimeLogGrep.grepRuntimeLogFiles({ keywords: ['grepneedle'], limit: 10 })
  assert.equal(longGrep.available, true, '长 grep 关键词应可搜索')
  assert.equal(longGrep.items.length, 1, 'grep 应跳过超过安全行长的命中，只返回正常日志')
  assert.equal(longGrep.items[0]?.event, 'long_grep_keyword_event')
  assert(!longGrep.items.some((item) => item.event === 'large_grep_keyword_event'), 'grep 不应把超长命中行输出到 Node 侧解析')

  console.log('运行日志搜索保护回归通过：短关键词不扫描，大行不输出，rawJson 入库有兜底截断')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
