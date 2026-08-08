import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AuditLogInput, OperationLogInput } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-log-list-page-window-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'log-list-page-window-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, runtimeLogIndexRepository, runtimeLogQueryRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/runtime-log-index.repository.js'),
  import('../../storage/runtime-log-query.repository.js')
])
const runtimeLogsRepository = { ...runtimeLogIndexRepository, ...runtimeLogQueryRepository }

try {
  repositories.createAuditLogsBatch([
    auditLog('audit_page_window_success', true, 'success', '2026-02-02T00:00:00.000Z'),
    auditLog('audit_page_window_failure', false, 'upstream_failed', '2026-02-02T00:00:01.000Z')
  ])
  repositories.createOperationLogsBatch([
    operationLog('op_page_window_admin', '2026-02-02T00:10:00.000Z'),
    operationLog('op_page_window_viewer', '2026-02-02T00:10:01.000Z')
  ])
  runtimeLogsRepository.createRuntimeLogsBatch([
    runtimeLog('rt_page_window_0', '2026-02-02T00:20:00.000Z'),
    runtimeLog('rt_page_window_1', '2026-02-02T00:20:01.000Z')
  ])

  const datasetDatabase = databaseModule.getDatasetDatabase()
  const originalPrepare = datasetDatabase.prepare.bind(datasetDatabase) as typeof datasetDatabase.prepare
  const calls: Array<{ sql: string; params: unknown[] }> = []
  datasetDatabase.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (isListSql(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        calls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof datasetDatabase.prepare

  try {
    const auditLogs = repositories.listAuditLogs({ page: 999999, pageSize: 1 })
    assert.equal(auditLogs.page, 1000, '审计日志深翻页应收敛到 1000 页以内')

    const auditErrorGroups = repositories.listAuditErrorGroups({ page: 999999, pageSize: 1 })
    assert.equal(auditErrorGroups.page, 1000, '审计错误组深翻页应收敛到 1000 页以内')

    const operationLogs = repositories.listOperationLogs({ page: 999999, pageSize: 1 })
    assert.equal(operationLogs.page, 1000, '操作日志管理列表深翻页应收敛到 1000 页以内')

    const viewerOperationLogs = repositories.listOperationLogsForViewer('sys_user', { page: 999999, pageSize: 1 })
    assert.equal(viewerOperationLogs.page, 1000, '操作日志用户可见列表深翻页应收敛到 1000 页以内')

    const runtimeLogs = runtimeLogsRepository.listRuntimeLogs({ page: 999999, pageSize: 1 })
    assert.equal(runtimeLogs.page, 1000, '运行日志深翻页应收敛到 1000 页以内')
  } finally {
    datasetDatabase.prepare = originalPrepare
  }

  assert(calls.length >= 5, '回归应捕获所有高增长日志列表 SQL')
  for (const call of calls) {
    assert(!/\bUNION\s+ALL\b/i.test(call.sql), `操作日志用户侧列表不应通过 UNION 后全局排序，SQL: ${call.sql}`)
    if (/\bOFFSET\s+\?/i.test(call.sql)) {
      const offset = Number(call.params.at(-1))
      const limit = Number(call.params.at(-2))
      assert.equal(limit, 2, `pageSize=1 时带 offset 的列表 SQL 只应多取 1 条，SQL: ${call.sql}`)
      assert(offset <= 999, `高增长日志列表 offset 必须被固定窗口限制在 1000 行内，实际 offset=${offset}，SQL: ${call.sql}`)
      continue
    }
    const limit = Number(call.params.at(-1))
    assert(limit <= 1001, `无 offset 的合并窗口 SQL 必须限制在 1001 行内，实际 limit=${limit}，SQL: ${call.sql}`)
  }
  for (const call of calls.filter((item) => /\boperation_log_(?:viewers|search_terms)\b|\bidx_operation_logs_visibility_created\b/i.test(item.sql))) {
    const plan = explainQueryPlan(datasetDatabase, call.sql, call.params)
    assertNoTempBtree(plan, `操作日志用户侧分支查询不应建立临时排序，SQL: ${call.sql}`)
    if (/\bFROM\s+operation_log_viewers\s+visible\b/i.test(call.sql)) {
      assertPlanUses(plan, 'idx_operation_log_viewers_account_created', '用户侧 targeted 操作日志必须由 viewer + created_at 索引驱动')
    }
    if (/\bidx_operation_logs_visibility_created\b/i.test(call.sql)) {
      assertPlanUses(plan, 'idx_operation_logs_visibility_created', '用户侧 all_users 操作日志必须由 visibility + created_at 索引驱动')
    }
  }

  console.log('高增长日志列表页码窗口回归通过：审计、操作和运行日志深翻页 offset / 合并窗口被限制在 1000 行级别')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function isListSql(sql: string): boolean {
  return /\bFROM\s+audit_logs\s+al\b[\s\S]*\bLIMIT\s+\?\s+OFFSET\s+\?/i.test(sql)
    || /\bFROM\s+audit_error_groups\s+aeg\b[\s\S]*\bLIMIT\s+\?\s+OFFSET\s+\?/i.test(sql)
    || /\bFROM\s+operation_logs\s+ol\b[\s\S]*\bLIMIT\s+\?\s+OFFSET\s+\?/i.test(sql)
    || /\bFROM\s+operation_log_viewers\s+visible\b[\s\S]*\bLIMIT\s+\?/i.test(sql)
    || /\bFROM\s+operation_logs\s+ol\s+INDEXED\s+BY\s+idx_operation_logs_visibility_created\b[\s\S]*\bLIMIT\s+\?/i.test(sql)
    || /\bFROM\s+runtime_logs\s+rl\b[\s\S]*\bLIMIT\s+\?\s+OFFSET\s+\?/i.test(sql)
}

function explainQueryPlan(database: ReturnType<typeof databaseModule.getDatasetDatabase>, sql: string, params: unknown[]): string[] {
  const queryParams = params as SQLInputValue[]
  const rows = database.prepare(`EXPLAIN QUERY PLAN ${sql}`).all(...queryParams) as Array<{ detail?: string }>
  return rows.map((row) => String(row.detail ?? ''))
}

function assertPlanUses(plan: string[], indexName: string, message: string): void {
  assert(plan.some((detail) => detail.includes(indexName)), `${message}：${plan.join(' | ')}`)
}

function assertNoTempBtree(plan: string[], message: string): void {
  assert(!plan.some((detail) => /USE TEMP B-TREE/i.test(detail)), `${message}：${plan.join(' | ')}`)
}

function auditLog(
  id: string,
  success: boolean,
  auditOutcome: AuditLogInput['auditOutcome'],
  createdAt: string
): AuditLogInput {
  return {
    id,
    traceId: `trace-${id}`,
    method: 'POST',
    path: '/v1/responses',
    model: 'gpt-5.5',
    stream: false,
    clientIp: '127.0.0.1',
    auditOutcome,
    success,
    finalStatusCode: success ? 200 : 503,
    errorPhase: success ? undefined : 'upstream',
    errorCode: success ? undefined : 'server_error',
    errorMessage: success ? undefined : '上游失败',
    sampleBucket: 1,
    sampleReason: 'regression',
    startedAt: createdAt,
    endedAt: createdAt,
    attempts: [],
    payloads: [],
    createdAt
  }
}

function operationLog(id: string, createdAt: string): OperationLogInput {
  return {
    id,
    actorSystemAccountId: 'sys_admin',
    actorUsername: 'admin',
    actorDisplayName: '管理员',
    actorRole: 'admin',
    module: 'regression',
    action: 'page_window',
    operationKey: 'regression.log_page_window',
    resourceType: 'log',
    resourceId: id,
    summary: '高增长日志页码窗口回归',
    viewers: [{ systemAccountId: 'sys_user', visibilityReason: 'resource_owner' }],
    createdAt
  }
}

function runtimeLog(id: string, time: string) {
  return {
    id,
    time,
    level: 'info',
    traceId: `trace-${id}`,
    event: 'log_page_window',
    message: '高增长日志页码窗口回归',
    rawJson: JSON.stringify({ time, level: 'info', msg: '高增长日志页码窗口回归' }),
    createdAt: time
  }
}
