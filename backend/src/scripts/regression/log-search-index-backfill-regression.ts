import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-log-search-backfill-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'log-search-backfill-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, operationLogsRepository, runtimeLogsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/operation-logs.repository.js'),
  import('../../storage/runtime-logs.repository.js')
])

try {
  const recordDatabase = databaseModule.getRecordDatabase()
  seedLegacyOperationLog('oplog_legacy_search_1', 'operationlegacyone', '2026-01-01T00:00:00.000Z')
  seedLegacyOperationLog('oplog_legacy_search_2', 'operationlegacytwo', '2026-01-01T00:00:01.000Z')

  assert.equal(operationLogsRepository.listOperationLogs({ keyword: 'operationlegacyone', pageSize: 10 }).items.length, 0, '缺少 FTS 行时历史操作日志不应被关键词搜到')
  const firstOperationBackfill = operationLogsRepository.backfillOperationLogSearchIndex(1)
  assert.equal(firstOperationBackfill.processed, 1, '操作日志搜索索引回填应按批次处理')
  assert.equal(firstOperationBackfill.hasMore, true, '操作日志第一批后应保留游标继续处理')
  assert.equal(operationLogsRepository.listOperationLogs({ keyword: 'operationlegacyone', pageSize: 10 }).items.length, 1, '第一批操作日志回填后应可搜索')
  assert.equal(operationLogsRepository.listOperationLogs({ keyword: 'operationlegacytwo', pageSize: 10 }).items.length, 0, '未处理到的操作日志不能被提前伪造为已索引')

  const secondOperationBackfill = operationLogsRepository.backfillOperationLogSearchIndex(1)
  assert.equal(secondOperationBackfill.processed, 1, '操作日志第二批应继续从游标推进')
  assert.equal(secondOperationBackfill.hasMore, false, '操作日志第二批后应追平历史游标')
  assert.equal(operationLogsRepository.listOperationLogs({ keyword: 'operationlegacytwo', pageSize: 10 }).items.length, 1, '第二批操作日志回填后应可搜索')
  assert.equal(operationLogsRepository.backfillOperationLogSearchIndex(1).processed, 0, '操作日志游标追平后不应重复处理旧批次')
  assert.equal(searchRowCount('operation_log_search'), 2, '操作日志回填应先删后插，不能制造重复 FTS 行')

  seedLegacyRuntimeLog('rtlog_legacy_search_1', 'runtimelegacyone', '2026-01-01T00:00:00.000Z')
  seedLegacyRuntimeLog('rtlog_legacy_search_2', 'runtimelegacytwo', '2026-01-01T00:00:01.000Z')

  assert.equal(runtimeLogsRepository.listRuntimeLogs({ keyword: 'runtimelegacyone', pageSize: 10 }).items.length, 0, '缺少 FTS 行时历史运行日志不应被关键词搜到')
  const firstRuntimeBackfill = runtimeLogsRepository.backfillRuntimeLogSearchIndex(1)
  assert.equal(firstRuntimeBackfill.processed, 1, '运行日志搜索索引回填应按批次处理')
  assert.equal(firstRuntimeBackfill.hasMore, true, '运行日志第一批后应保留游标继续处理')
  assert.equal(runtimeLogsRepository.listRuntimeLogs({ keyword: 'runtimelegacyone', pageSize: 10 }).items.length, 1, '第一批运行日志回填后应可搜索')
  assert.equal(runtimeLogsRepository.listRuntimeLogs({ keyword: 'runtimelegacytwo', pageSize: 10 }).items.length, 0, '未处理到的运行日志不能被提前伪造为已索引')

  const secondRuntimeBackfill = runtimeLogsRepository.backfillRuntimeLogSearchIndex(1)
  assert.equal(secondRuntimeBackfill.processed, 1, '运行日志第二批应继续从游标推进')
  assert.equal(secondRuntimeBackfill.hasMore, false, '运行日志第二批后应追平历史游标')
  assert.equal(runtimeLogsRepository.listRuntimeLogs({ keyword: 'runtimelegacytwo', pageSize: 10 }).items.length, 1, '第二批运行日志回填后应可搜索')
  assert.equal(runtimeLogsRepository.backfillRuntimeLogSearchIndex(1).processed, 0, '运行日志游标追平后不应重复处理旧批次')
  assert.equal(searchRowCount('runtime_log_search'), 2, '运行日志回填应先删后插，不能制造重复 FTS 行')

  const stateRows = recordDatabase
    .prepare("SELECT job_name, cursor_id, last_error_message FROM stats_job_state WHERE job_name IN ('operation_log_search_backfill', 'runtime_log_search_backfill') ORDER BY job_name")
    .all() as Array<{ job_name: string; cursor_id?: string | null; last_error_message?: string | null }>
  assert.deepEqual(stateRows.map((row) => row.job_name), ['operation_log_search_backfill', 'runtime_log_search_backfill'], '搜索索引回填应复用 stats_job_state 记录游标')
  assert(stateRows.every((row) => row.cursor_id?.endsWith('_legacy_search_2') && !row.last_error_message), '搜索索引回填完成后应推进到第二条且清空错误')

  seedStaleBackfillCursor('operation_log_search_backfill', '1999-01-01T00:00:00.000Z', 'stale_operation_cursor')
  const operationNoOp = operationLogsRepository.backfillOperationLogSearchIndex(1)
  assert.equal(operationNoOp.processed, 0, '操作日志搜索索引已对齐时不应继续重写已索引行')
  assert.equal(operationNoOp.hasMore, false, '操作日志搜索索引已对齐时应直接结束')
  assert.equal(operationNoOp.cursorCreatedAt, '2026-01-01T00:00:01.000Z', '操作日志搜索索引已对齐时应把游标推进到源表最新行')
  assert.equal(operationNoOp.cursorId, 'oplog_legacy_search_2', '操作日志搜索索引已对齐时应把游标推进到源表最新 ID')

  seedStaleBackfillCursor('runtime_log_search_backfill', '1999-01-01T00:00:00.000Z', 'stale_runtime_cursor')
  const runtimeNoOp = runtimeLogsRepository.backfillRuntimeLogSearchIndex(1)
  assert.equal(runtimeNoOp.processed, 0, '运行日志搜索索引已对齐时不应继续重写已索引行')
  assert.equal(runtimeNoOp.hasMore, false, '运行日志搜索索引已对齐时应直接结束')
  assert.equal(runtimeNoOp.cursorCreatedAt, '2026-01-01T00:00:01.000Z', '运行日志搜索索引已对齐时应把游标推进到源表最新行')
  assert.equal(runtimeNoOp.cursorId, 'rtlog_legacy_search_2', '运行日志搜索索引已对齐时应把游标推进到源表最新 ID')

  const backfillStatesAfterNoOp = recordDatabase
    .prepare(`
      SELECT job_name, cursor_id, lag_seconds, last_error_message
      FROM stats_job_state
      WHERE job_name IN ('operation_log_search_backfill', 'runtime_log_search_backfill')
      ORDER BY job_name
    `)
    .all() as Array<{ job_name: string; cursor_id?: string | null; lag_seconds?: number | null; last_error_message?: string | null }>
  assert(backfillStatesAfterNoOp.every((row) => row.cursor_id?.endsWith('_legacy_search_2') && row.lag_seconds === null && !row.last_error_message), '已对齐的搜索索引回填应清空 lag_seconds 并把游标推进到源表最新行')

  console.log('日志搜索索引回填回归通过：历史 operation/runtime logs 分批补齐 FTS 且不重复命中')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedLegacyOperationLog(id: string, needle: string, createdAt: string): void {
  databaseModule.getRecordDatabase().prepare(`
    INSERT INTO operation_logs (
      id, trace_id, actor_system_account_id, actor_username, actor_display_name, actor_role,
      operation_scope_system_account_id, mode, module, action, operation_key, resource_type, resource_id,
      resource_name, summary, detail_level, visibility_scope, changes_json, metadata_json, method, path,
      status_code, client_ip, user_agent, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    id,
    `trace_${id}`,
    'sys_admin',
    'admin',
    '管理员',
    'admin',
    'sys_admin',
    'self',
    'regression',
    'legacy_search_backfill',
    'regression.legacy_search_backfill',
    'regression',
    id,
    `历史操作日志 ${needle}`,
    `历史操作日志搜索索引回填 ${needle}`,
    'full',
    'targeted',
    '[]',
    '{}',
    'POST',
    '/regression/log-search-backfill',
    200,
    '127.0.0.1',
    'log-search-backfill-regression',
    createdAt
  )
}

function seedLegacyRuntimeLog(id: string, needle: string, createdAt: string): void {
  databaseModule.getRecordDatabase().prepare(`
    INSERT INTO runtime_logs (
      id, time, level, trace_id, event, message, error_message, raw_json, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    id,
    createdAt,
    'info',
    `trace_${id}`,
    `legacy_runtime_${needle}`,
    `历史运行日志搜索索引回填 ${needle}`,
    null,
    JSON.stringify({ time: createdAt, level: 'info', event: `legacy_runtime_${needle}`, msg: `历史运行日志搜索索引回填 ${needle}` }),
    createdAt
  )
}

function searchRowCount(tableName: 'operation_log_search' | 'runtime_log_search'): number {
  const row = databaseModule.getRecordDatabase().prepare(`SELECT COUNT(*) AS total FROM ${tableName}`).get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function seedStaleBackfillCursor(jobName: 'operation_log_search_backfill' | 'runtime_log_search_backfill', cursorCreatedAt: string, cursorId: string): void {
  databaseModule.getRecordDatabase()
    .prepare(`
      UPDATE stats_job_state
      SET cursor_created_at = ?, cursor_id = ?, last_error_message = NULL, lag_seconds = 999, updated_at = ?
      WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
    `)
    .run(cursorCreatedAt, cursorId, cursorCreatedAt, jobName)
}
