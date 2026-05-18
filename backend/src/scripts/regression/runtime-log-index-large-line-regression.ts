import { strict as assert } from 'node:assert'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-runtime-log-index-large-line-'))
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.processRole = 'worker'

const [databaseModule, runtimeLogIndexQueue, runtimeLogsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/runtime-logs/runtime-log-index-queue.service.js'),
  import('../../storage/runtime-logs.repository.js')
])

try {
  const now = new Date().toISOString()
  runtimeLogIndexQueue.enqueueRuntimeLogLineLocal(JSON.stringify({
    time: now,
    level: 30,
    event: 'normal_runtime_log_event',
    msg: '普通日志 needle'
  }))
  runtimeLogIndexQueue.enqueueRuntimeLogLineLocal(JSON.stringify({
    time: now,
    level: 30,
    event: 'large_runtime_log_event',
    msg: '超长日志 needle',
    body: 'x'.repeat(150 * 1024)
  }))

  runtimeLogIndexQueue.flushAllRuntimeLogIndexQueue()

  const normal = runtimeLogsRepository.listRuntimeLogs({ keyword: '普通日志', pageSize: 10 })
  assert.equal(normal.items.length, 1, '普通日志应可索引并搜索')
  assert.equal(normal.items[0]?.event, 'normal_runtime_log_event', '普通日志应提取结构字段')

  const large = runtimeLogsRepository.listRuntimeLogs({ keyword: '超长日志', pageSize: 10 })
  assert.equal(large.items.length, 1, '超长日志应保留截断 rawJson 并可搜索')
  assert.equal(large.items[0]?.event, undefined, '超长日志不应同步解析整行 JSON 提取字段')
  const largeDetail = runtimeLogsRepository.getRuntimeLogDetail(large.items[0]?.id ?? '')
  assert(largeDetail?.rawJson.includes('[truncated]'), '超长日志 rawJson 应带截断标记')
  assert((largeDetail?.rawJson.length ?? 0) < 140 * 1024, '超长日志 rawJson 应在索引前截断')

  const facets = runtimeLogsRepository.getRuntimeLogFacets()
  assert.equal(facets.totalIndexed, 2, '运行日志 facets 应读取预聚合总量')
  assert.equal(facets.levels.find((item) => item.value === 'info')?.count, 2, '运行日志 facets 应维护 level 预聚合')
  assert(facets.events.includes('normal_runtime_log_event'), '运行日志 facets 应维护事件列表')

  const deleted = runtimeLogsRepository.cleanupRuntimeLogIndex(new Date(Date.now() + 1000).toISOString(), 10)
  assert.equal(deleted, 2, '运行日志清理应删除过期索引')
  const facetsAfterCleanup = runtimeLogsRepository.getRuntimeLogFacets()
  assert.equal(facetsAfterCleanup.totalIndexed, 0, '运行日志清理应同步扣减 facets 总量')
  assert.equal(facetsAfterCleanup.levels.length, 0, '运行日志清理应删除空 level facet')
  assert.equal(facetsAfterCleanup.events.length, 0, '运行日志清理应删除空 event facet')

  console.log('运行日志索引超长行回归通过：超长日志不再同步完整解析，仍保留截断原文搜索')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
