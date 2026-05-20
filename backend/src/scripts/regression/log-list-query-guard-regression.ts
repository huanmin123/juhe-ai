import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AuditLogInput, OperationLogInput } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-log-list-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'log-list-query-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  repositories.createAuditLogsBatch([
    auditLog({
      id: 'audit_log_guard_exact',
      traceId: 'trace-log-list-guard-exact',
      path: '/v1/responses',
      model: 'gpt-5.5',
      createdAt: '2026-02-01T00:00:00.000Z'
    }),
    auditLog({
      id: 'audit_log_guard_prefix_only',
      traceId: 'trace-log-list-guard-prefix-only',
      path: '/v1/responses/extra',
      model: 'gpt-5.5-mini',
      createdAt: '2026-02-01T00:00:01.000Z'
    }),
    auditLog({
      id: 'audit_log_guard_error_group',
      traceId: 'trace-log-list-guard-error-group',
      path: '/v1/responses',
      model: 'gpt-5.5',
      statusCode: 503,
      auditOutcome: 'upstream_failed',
      success: false,
      errorPhase: 'upstream',
      errorCode: 'server_error',
      errorMessage: '上游服务暂不可用',
      createdAt: '2026-02-01T00:00:02.000Z'
    })
  ])

  repositories.createOperationLogsBatch([
    operationLog({
      id: 'op_log_guard_fts_match',
      traceId: 'trace-op-list-guard-match',
      summary: '更新 keywordguardneedle 相关 API Key 配置',
      resourceId: 'resource_keywordguardneedle',
      resourceName: 'keywordguardneedle 资源',
      actorDisplayName: '管理员甲',
      createdAt: '2026-02-01T00:10:00.000Z',
      viewers: [{ systemAccountId: 'sys_user', visibilityReason: 'resource_owner' }]
    }),
    operationLog({
      id: 'op_log_guard_fts_miss',
      traceId: 'trace-op-list-guard-miss',
      summary: '更新普通资源配置',
      resourceId: 'resource_plain',
      resourceName: '普通资源',
      actorDisplayName: '管理员乙',
      createdAt: '2026-02-01T00:20:00.000Z',
      viewers: [{ systemAccountId: 'sys_user', visibilityReason: 'resource_owner' }]
    }),
    operationLog({
      id: 'op_log_guard_short_keyword',
      traceId: 'trace-op-list-guard-short',
      summary: '造数资源变更',
      resourceId: 'resource_mockdata',
      resourceName: '造数资源',
      actorDisplayName: '管理员丙',
      createdAt: '2026-02-01T00:30:00.000Z',
      viewers: [{ systemAccountId: 'sys_user', visibilityReason: 'resource_owner' }]
    }),
    operationLog({
      id: 'op_log_guard_short_keyword_middle',
      traceId: 'trace-op-list-guard-short-middle',
      summary: '更新造数资源',
      resourceId: 'resource_plain_mockdata',
      resourceName: '普通造数资源',
      actorDisplayName: '管理员丁',
      createdAt: '2026-02-01T00:40:00.000Z',
      viewers: [{ systemAccountId: 'sys_user', visibilityReason: 'resource_owner' }]
    })
  ])

  const recordDatabase = databaseModule.getRecordDatabase()
  const originalPrepare = recordDatabase.prepare.bind(recordDatabase) as typeof recordDatabase.prepare
  const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
  recordDatabase.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const shouldCapture = /\bFROM\s+(audit_logs|audit_error_groups|operation_logs)\s+(al|aeg|ol)\b/i.test(sql)
      || /\bFROM\s*\(\s*SELECT[\s\S]*\bFROM\s+operation_logs\s+ol\b/i.test(sql)
    if (shouldCapture) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof recordDatabase.prepare

  try {
    const auditPath = repositories.listAuditLogs({ path: '/v1/responses', pageSize: 10 })
    assert.deepEqual(
      auditPath.items.map((item) => item.id).sort(),
      ['audit_log_guard_error_group', 'audit_log_guard_exact'],
      '审计 path 筛选应精确匹配，不应命中相似路径'
    )

    const auditModel = repositories.listAuditLogs({ model: 'gpt-5.5', pageSize: 10 })
    assert(!auditModel.items.some((item) => item.model === 'gpt-5.5-mini'), '审计 model 筛选应精确匹配，不应命中模型名前缀')

    const auditTracePrefix = repositories.listAuditLogs({ traceId: 'trace-log-list-guard', pageSize: 10 })
    assert.equal(auditTracePrefix.items.length, 3, '审计 traceId 筛选应支持右侧前缀定位')

    const errorGroups = repositories.listAuditErrorGroups({ path: '/v1/responses', model: 'gpt-5.5', statusCode: 503, pageSize: 10 })
    assert.equal(errorGroups.items.length, 1, '审计错误组 path/model/statusCode 应按结构化条件定位')

    const operationKeyword = repositories.listOperationLogs({ keyword: 'keywordguardneedle', pageSize: 10 })
    assert.deepEqual(operationKeyword.items.map((item) => item.id), ['op_log_guard_fts_match'], '操作日志长关键词应通过 FTS 命中自由文本')

    const viewerKeyword = repositories.listOperationLogsForViewer('sys_user', { keyword: 'keywordguardneedle', pageSize: 10 })
    assert.deepEqual(viewerKeyword.items.map((item) => item.id), ['op_log_guard_fts_match'], '用户侧操作日志关键词也应通过 FTS 保留可见性过滤')

    const shortKeywordWithoutWindow = repositories.listOperationLogs({ keyword: '造数', pageSize: 10 })
    assert.equal(shortKeywordWithoutWindow.items.length, 0, '短关键词没有小时间窗时不应退回无边界 LIKE 扫描')
  } finally {
    recordDatabase.prepare = originalPrepare
  }

  assert(capturedCalls.length >= 7, '回归应捕获日志列表 SQL')
  for (const call of capturedCalls) {
    assert(!/\b(?:al|aeg)\.[a-z_]+\s+LIKE\s+\?/i.test(call.sql), '审计日志和错误组列表不应使用 LIKE 扫描结构化字段')
    assert(!/\bol\.trace_id\s+LIKE\s+\?/i.test(call.sql), '操作日志 traceId 不应使用 LIKE 扫描')
    assert(!/\bol\.(summary|resource_name|actor_display_name|actor_username)\s+LIKE\s+\?/i.test(call.sql), '操作日志关键词不应使用多列 LIKE 扫描')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '无边界日志列表查询不应传入前导通配符参数')
  }
  assert(capturedCalls.some((call) => /\bINNER\s+JOIN\s+operation_log_search\b/i.test(call.sql) && /\boperation_log_search\s+MATCH\s+\?/i.test(call.sql)), '操作日志长关键词应走 operation_log_search FTS')

  const boundedCalls: Array<{ sql: string; params: unknown[] }> = []
  const boundedRecordDatabase = databaseModule.getRecordDatabase()
  const boundedOriginalPrepare = boundedRecordDatabase.prepare.bind(boundedRecordDatabase) as typeof boundedRecordDatabase.prepare
  boundedRecordDatabase.prepare = ((sql: string) => {
    const statement = boundedOriginalPrepare(sql)
    if (/\bFROM\s+operation_logs\s+ol\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        boundedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof boundedRecordDatabase.prepare
  try {
    const shortKeywordWithWindow = repositories.listOperationLogs({
      keyword: '造数',
      startAt: '2026-02-01T00:00:00.000Z',
      endAt: '2026-02-01T01:00:00.000Z',
      pageSize: 10
    })
    assert.deepEqual(shortKeywordWithWindow.items.map((item) => item.id), ['op_log_guard_short_keyword'], '短关键词只允许在明确小时间窗内做精确或前缀文本检索')
  } finally {
    boundedRecordDatabase.prepare = boundedOriginalPrepare
  }
  assert(boundedCalls.some((call) => /\bol\.created_at\s+>=\s+\?/i.test(call.sql)
    && /\bol\.created_at\s+<=\s+\?/i.test(call.sql)
    && /\bol\.summary\s+COLLATE\s+NOCASE\s+>=\s+\?/i.test(call.sql)), '短关键词前缀检索必须绑定小时间窗')
  for (const call of boundedCalls) {
    assert(!/\bol\.(summary|resource_name|actor_display_name|actor_username)\s+LIKE\s+\?/i.test(call.sql), '小时间窗操作日志关键词也不应使用多列 LIKE 扫描')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '小时间窗操作日志关键词不应传入前导通配符参数')
  }

  console.log('日志列表查询防护回归通过：审计结构化过滤无前导通配符，操作日志长关键词走 FTS，短关键词仅允许小时间窗精确或前缀检索')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function auditLog(input: {
  id: string
  traceId: string
  path: string
  model: string
  createdAt: string
  statusCode?: number
  auditOutcome?: AuditLogInput['auditOutcome']
  success?: boolean
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
}): AuditLogInput {
  return {
    id: input.id,
    traceId: input.traceId,
    systemAccountId: 'sys_admin',
    providerCode: 'openai',
    method: 'POST',
    path: input.path,
    model: input.model,
    stream: false,
    clientIp: '127.0.0.1',
    auditOutcome: input.auditOutcome ?? 'success',
    success: input.success ?? true,
    finalStatusCode: input.statusCode ?? 200,
    errorPhase: input.errorPhase,
    errorCode: input.errorCode,
    errorMessage: input.errorMessage,
    sampleBucket: 1,
    sampleReason: 'regression',
    startedAt: input.createdAt,
    endedAt: input.createdAt,
    durationMs: 1,
    attempts: [],
    payloads: [],
    createdAt: input.createdAt
  }
}

function operationLog(input: {
  id: string
  traceId: string
  summary: string
  resourceId: string
  resourceName: string
  actorDisplayName: string
  createdAt: string
  viewers: OperationLogInput['viewers']
}): OperationLogInput {
  return {
    id: input.id,
    traceId: input.traceId,
    actorSystemAccountId: 'sys_admin',
    actorUsername: 'admin',
    actorDisplayName: input.actorDisplayName,
    actorRole: 'admin',
    operationScopeSystemAccountId: 'sys_admin',
    mode: 'admin',
    module: 'api_keys',
    action: 'update',
    operationKey: `api_keys.update.${input.resourceId}`,
    resourceType: 'api_key',
    resourceId: input.resourceId,
    resourceName: input.resourceName,
    summary: input.summary,
    detailLevel: 'full',
    visibilityScope: 'targeted',
    changes: [],
    metadata: {},
    method: 'PATCH',
    path: `/__aisys__/api/api-keys/${input.resourceId}`,
    statusCode: 200,
    clientIp: '127.0.0.1',
    userAgent: 'log-list-query-guard',
    viewers: input.viewers,
    createdAt: input.createdAt
  }
}
