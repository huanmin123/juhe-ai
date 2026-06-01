import { strict as assert } from 'node:assert'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-search-guard-'))
const logDir = join(tempRoot, 'logs')
mkdirSync(logDir)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
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
  const oldTime = new Date(Date.parse(now) - 10_000).toISOString()
  runtimeLogsRepository.createRuntimeLogsBatch([
    {
      id: 'rtlog_facet_cleanup_guard',
      time: oldTime,
      level: 'info',
      traceId: 'trace-runtime-cleanup-guard',
      event: 'facet_cleanup_guard_event',
      message: 'cleanup guard',
      rawJson: JSON.stringify({
        time: oldTime,
        level: 'info',
        traceId: 'trace-runtime-cleanup-guard',
        event: 'facet_cleanup_guard_event',
        msg: 'cleanup guard'
      }),
      createdAt: oldTime
    },
    {
      id: 'rtlog_short_keyword_guard',
      time: now,
      level: 'info',
      traceId: 'trace-runtime-guard-short',
      event: 'short_keyword_guard_event',
      message: '单字消息 x',
      rawJson: JSON.stringify({
        time: now,
        level: 'info',
        traceId: 'trace-runtime-guard-short',
        event: 'short_keyword_guard_event',
        msg: '单字消息 x',
        body: 'x'.repeat(150 * 1024)
      }),
      createdAt: now
    },
    {
      id: 'rtlog_long_keyword_guard',
      time: now,
      level: 'info',
      traceId: 'trace-runtime-guard-long',
      event: 'long_keyword_guard_event',
      message: '前缀 guardneedle 后缀',
      rawJson: JSON.stringify({
        time: now,
        level: 'info',
        traceId: 'trace-runtime-guard-long',
        event: 'long_keyword_guard_event',
        msg: 'guardneedle'
      }),
      createdAt: now
    },
    {
      id: 'rtlog_raw_json_keyword_guard',
      time: now,
      level: 'info',
      traceId: 'trace-runtime-guard-raw-json',
      event: 'raw_json_keyword_guard_event',
      message: '普通消息',
      rawJson: JSON.stringify({
        time: now,
        level: 'info',
        traceId: 'trace-runtime-guard-raw-json',
        event: 'raw_json_keyword_guard_event',
        msg: '普通消息',
        body: 'rawjsonneedle'
      }),
      createdAt: now
    }
  ])

  const shortKeyword = runtimeLogsRepository.listRuntimeLogs({ keyword: 'x', pageSize: 10 })
  assert.equal(shortKeyword.items.length, 1, '运行日志 keyword 应直接在 message 列做模糊匹配，短关键词也允许')
  assert.equal(shortKeyword.items[0]?.id, 'rtlog_short_keyword_guard')

  const longKeyword = runtimeLogsRepository.listRuntimeLogs({ keyword: 'guardneedle', pageSize: 10 })
  assert.equal(longKeyword.items.length, 1, '运行日志 keyword 应命中 message 中间内容')
  assert.equal(longKeyword.items[0]?.event, 'long_keyword_guard_event')

  const rawJsonKeyword = runtimeLogsRepository.listRuntimeLogs({ keyword: 'rawjsonneedle', pageSize: 10 })
  assert.equal(rawJsonKeyword.items.length, 0, '运行日志 keyword 不应检索 raw_json 正文')

  const tracePrefix = runtimeLogsRepository.listRuntimeLogs({ traceId: 'trace-runtime-guard', pageSize: 10 })
  assert.deepEqual(
    tracePrefix.items.map((item) => item.traceId).sort(),
    ['trace-runtime-guard-long', 'trace-runtime-guard-raw-json', 'trace-runtime-guard-short'],
    '运行日志 traceId 筛选应支持右侧前缀定位，与审计/操作日志契约一致'
  )

  const datasetDatabase = databaseModule.getDatasetDatabase()
  const originalPrepare = datasetDatabase.prepare.bind(datasetDatabase) as typeof datasetDatabase.prepare
  const facetMaintenanceSql: string[] = []
  datasetDatabase.prepare = ((sql: string) => {
    facetMaintenanceSql.push(sql)
    return originalPrepare(sql)
  }) as typeof datasetDatabase.prepare
  try {
    datasetDatabase.prepare('DELETE FROM runtime_log_facet_summary').run()
    runtimeLogsRepository.ensureRuntimeLogFacetSnapshots()
    runtimeLogsRepository.cleanupRuntimeLogIndex(new Date(Date.parse(now) - 1).toISOString(), 10)
  } finally {
    datasetDatabase.prepare = originalPrepare
  }
  assert(
    !facetMaintenanceSql.some((sql) => /\bFROM\s+runtime_logs\b[\s\S]*\bGROUP\s+BY\b/i.test(sql)),
    '运行日志 facets 维护不应在缺快照或清理路径对 runtime_logs 做 GROUP BY 聚合扫描'
  )
  assert(
    !facetMaintenanceSql.some((sql) => /\bSELECT\s+MIN\(time\)[\s\S]*\bFROM\s+runtime_logs\b/i.test(sql)),
    '运行日志清理不应为了更新 facets 对保留窗口做 MIN/MAX 聚合扫描'
  )

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

  const concurrentGrep = await Promise.all([
    runtimeLogGrep.grepRuntimeLogFiles({ keywords: ['grepneedle'], limit: 10 }),
    runtimeLogGrep.grepRuntimeLogFiles({ keywords: ['grepneedle'], limit: 10 })
  ])
  assert(concurrentGrep.some((result) => result.available), '并发 grep 中应有一个请求获得执行槽')
  assert(concurrentGrep.some((result) => !result.available && /已有 grep 搜索正在运行/.test(result.message ?? '')), '并发 grep 应拒绝额外请求，避免多个 rg 同时扫描日志目录')

  writeFileSync(join(logDir, 'juhe-ai-heavy.log'), Array.from({ length: 2_050 }, (_, index) => JSON.stringify({
    time: now,
    level: 30,
    event: 'parse_cap_guard_event',
    msg: `parsecapneedle-${index}`
  })).join('\n') + '\n')

  const cappedGrep = await runtimeLogGrep.grepRuntimeLogFiles({ keywords: ['parsecapneedle'], limit: 10 })
  assert.equal(cappedGrep.available, true, '触发解析上限的 grep 仍应返回结果')
  assert.equal(cappedGrep.items.length, 10, 'grep 结果应按请求上限返回最近命中')
  assert.equal(cappedGrep.truncated, true, '触发解析上限时应标记截断')
  assert.match(cappedGrep.message ?? '', /安全解析上限 2000/, '触发解析上限时应提示提前停止')

  console.log('运行日志搜索保护回归通过：索引模式仅模糊匹配 message，rawJson 不检索')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
