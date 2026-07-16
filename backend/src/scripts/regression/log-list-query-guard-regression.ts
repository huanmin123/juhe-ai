import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AuditLogInput, OperationLogInput } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-log-list-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
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
      id: 'op_log_guard_keyword_match',
      traceId: 'trace-op-list-guard-match',
      summary: '更新 keywordguardneedle 相关 API Key 配置',
      resourceId: 'resource_keywordguardneedle',
      resourceName: 'keywordguardneedle 资源',
      actorDisplayName: '管理员甲',
      createdAt: '2026-02-01T00:10:00.000Z',
      viewers: [{ systemAccountId: 'sys_user', visibilityReason: 'resource_owner' }]
    }),
    operationLog({
      id: 'op_log_guard_keyword_miss',
      traceId: 'trace-op-list-guard-miss',
      summary: '更新普通资源配置',
      resourceId: 'resource_plain',
      resourceName: '普通资源',
      actorDisplayName: '管理员乙',
      createdAt: '2026-02-01T00:20:00.000Z',
      viewers: [{ systemAccountId: 'sys_user', visibilityReason: 'resource_owner' }]
    }),
    operationLog({
      id: 'op_log_guard_structured_filter_only',
      traceId: 'trace-op-list-guard-structured',
      summary: '更新结构化筛选资源配置',
      resourceId: 'resource_resourceonlyneedle',
      resourceName: 'resourceonlyneedle 资源',
      actorDisplayName: 'resourceonlyneedle 管理员',
      createdAt: '2026-02-01T00:25:00.000Z',
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
    }),
    operationLog({
      id: 'op_log_guard_actor_operator',
      traceId: 'trace-op-list-guard-actor',
      summary: '用户操作人筛选资源变更',
      resourceId: 'resource_actor_operator',
      resourceName: '操作人筛选资源',
      actorSystemAccountId: 'sys_operator',
      actorUsername: 'operator',
      actorDisplayName: '操作员甲',
      createdAt: '2026-02-01T00:50:00.000Z',
      viewers: [{ systemAccountId: 'sys_user', visibilityReason: 'resource_owner' }]
    })
  ])

  const datasetDatabase = databaseModule.getDatasetDatabase()
  const originalPrepare = datasetDatabase.prepare.bind(datasetDatabase) as typeof datasetDatabase.prepare
  const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
  datasetDatabase.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const shouldCapture = /\bFROM\s+(audit_logs|audit_error_groups|operation_logs)\s+(al|aeg|ol)\b/i.test(sql)
      || /\bFROM\s+operation_log_summary_search_terms\s+search\b/i.test(sql)
      || /\bFROM\s*\(\s*SELECT[\s\S]*\bFROM\s+operation_logs\s+ol\b/i.test(sql)
    if (shouldCapture) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof datasetDatabase.prepare

  try {
    const auditPath = repositories.listAuditLogs({ path: '/v1/responses', pageSize: 10 })
    assert.deepEqual(
      auditPath.items.map((item) => item.id).sort(),
      ['audit_log_guard_error_group', 'audit_log_guard_exact'],
      '审计 path 筛选应精确匹配，不应命中相似路径'
    )
    const auditPathWithMethodAndQuery = repositories.listAuditLogs({ path: 'POST /v1/responses?include[]=input', pageSize: 10 })
    assert.deepEqual(
      auditPathWithMethodAndQuery.items.map((item) => item.id).sort(),
      ['audit_log_guard_error_group', 'audit_log_guard_exact'],
      '审计 path 筛选应兼容从接口列复制的 METHOD path?query 文本'
    )

    const auditModel = repositories.listAuditLogs({ model: 'gpt-5.5', pageSize: 10 })
    assert(!auditModel.items.some((item) => item.model === 'gpt-5.5-mini'), '审计 model 筛选应精确匹配，不应命中模型名前缀')

    const auditTracePrefix = repositories.listAuditLogs({ traceId: 'trace-log-list-guard', pageSize: 10 })
    assert.equal(auditTracePrefix.items.length, 3, '审计 traceId 筛选应支持右侧前缀定位')

    const auditClientIpPrefix = repositories.listAuditLogs({ clientIp: '127.0.', pageSize: 10 })
    assert.equal(auditClientIpPrefix.items.length, 3, '审计 clientIp 筛选应支持右侧前缀定位')

    const auditTimeWindow = repositories.listAuditLogs({
      startAt: '2026-02-01T00:00:01.000Z',
      endAt: '2026-02-01T00:00:02.000Z',
      pageSize: 10
    })
    assert.deepEqual(
      auditTimeWindow.items.map((item) => item.id),
      ['audit_log_guard_error_group', 'audit_log_guard_prefix_only'],
      '审计普通列表应支持 created_at 时间窗筛选'
    )

    const errorGroups = repositories.listAuditErrorGroups({ path: '/v1/responses', model: 'gpt-5.5', statusCode: 503, pageSize: 10 })
    assert.equal(errorGroups.items.length, 1, '审计错误组 path/model/statusCode 应按结构化条件定位')
    const errorGroupsWithMethodAndQuery = repositories.listAuditErrorGroups({ path: 'POST /v1/responses?include[]=input', model: 'gpt-5.5', statusCode: 503, pageSize: 10 })
    assert.equal(errorGroupsWithMethodAndQuery.items.length, 1, '审计错误组 path 筛选应兼容 METHOD path?query 文本')

    const operationKeyword = repositories.listOperationLogs({ summaryKeyword: 'keywordguardneedle', pageSize: 10 })
    assert.deepEqual(operationKeyword.items.map((item) => item.id), ['op_log_guard_keyword_match'], '操作日志摘要搜索应通过倒排词项命中中文摘要')

    const viewerKeyword = repositories.listOperationLogsForViewer('sys_user', { summaryKeyword: 'keywordguardneedle', pageSize: 10 })
    assert.deepEqual(viewerKeyword.items.map((item) => item.id), ['op_log_guard_keyword_match'], '用户侧操作日志摘要搜索也应通过倒排词项保留可见性过滤')

    const resourceOnlySummaryKeyword = repositories.listOperationLogs({ summaryKeyword: 'resourceonlyneedle', pageSize: 10 })
    assert.deepEqual(resourceOnlySummaryKeyword.items.map((item) => item.id), [], '操作日志摘要搜索不应命中资源名或操作人')

    const resourceIdFilter = repositories.listOperationLogs({ resourceId: 'resource_resourceonlyneedle', pageSize: 10 })
    assert.deepEqual(resourceIdFilter.items.map((item) => item.id), ['op_log_guard_structured_filter_only'], '资源 ID 应通过独立结构化筛选命中')

    const actorSystemAccountFilter = repositories.listOperationLogs({ actorSystemAccountId: 'sys_operator', pageSize: 10 })
    assert.deepEqual(actorSystemAccountFilter.items.map((item) => item.id), ['op_log_guard_actor_operator'], '操作日志管理应支持按用户操作人筛选')

    const shortKeywordWithoutWindow = repositories.listOperationLogs({ summaryKeyword: '造数', pageSize: 10 })
    assert.deepEqual(
      shortKeywordWithoutWindow.items.map((item) => item.id),
      ['op_log_guard_short_keyword_middle', 'op_log_guard_short_keyword'],
      '短中文摘要搜索应通过倒排词项命中，不回退扫描操作日志表字段'
    )

    const singleChineseKeyword = repositories.listOperationLogs({ summaryKeyword: '造', pageSize: 10 })
    assert.deepEqual(
      singleChineseKeyword.items.map((item) => item.id),
      ['op_log_guard_short_keyword_middle', 'op_log_guard_short_keyword'],
      '中文单字摘要搜索应通过倒排词项命中'
    )
    const singleEnglishKeyword = repositories.listOperationLogs({ summaryKeyword: 'k', pageSize: 10 })
    assert.deepEqual(singleEnglishKeyword.items.map((item) => item.id), ['op_log_guard_keyword_match'], '英文单字摘要搜索应通过倒排词项命中')
    const singleNumberKeyword = repositories.listOperationLogs({ summaryKeyword: '5', pageSize: 10 })
    assert.deepEqual(singleNumberKeyword.items.map((item) => item.id), [], '不存在的数字单字摘要搜索应返回空列表且不回退主表扫描')
  } finally {
    datasetDatabase.prepare = originalPrepare
  }

  assert(capturedCalls.length >= 7, '回归应捕获日志列表 SQL')
  for (const call of capturedCalls) {
    assert(!/\b(?:al|aeg)\.[a-z_]+\s+LIKE\s+\?/i.test(call.sql), '审计日志和错误组列表不应使用 LIKE 扫描结构化字段')
    assert(!/\bol\.[a-z_]+\s+LIKE\s+\?/i.test(call.sql), '操作日志列表不应使用主表 LIKE 扫描')
    assert(!/\bol\.trace_id\s+LIKE\s+\?/i.test(call.sql), '操作日志 traceId 不应使用 LIKE 扫描')
    assert(!/\bMATCH\s+\?/i.test(call.sql), '操作日志列表不应再使用 MATCH 查询')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '日志列表不应传入前导通配符参数')
  }
  const keywordSearchCalls = capturedCalls.filter((call) => /\bFROM\s+operation_log_summary_search_terms\s+search\b/i.test(call.sql))
  assert(keywordSearchCalls.some((call) => call.params.some((param) => param === 'keywordguardneedle')), '操作日志摘要搜索应使用摘要倒排词项表定位')
  assert(keywordSearchCalls.some((call) => call.params.some((param) => param === '造数')), '操作日志短中文摘要搜索应使用摘要倒排词项表定位')
  assert(keywordSearchCalls.some((call) => call.params.some((param) => param === '造')), '操作日志中文单字搜索应使用摘要倒排词项表定位')
  assert(keywordSearchCalls.some((call) => call.params.some((param) => param === 'k')), '操作日志英文单字搜索应使用摘要倒排词项表定位')
  assert(keywordSearchCalls.some((call) => call.params.some((param) => param === '5')), '操作日志数字单字搜索应使用摘要倒排词项表定位')
  assert(capturedCalls.some((call) => /\bol\.actor_system_account_id\s*=\s*\?/i.test(call.sql)
    && call.params.some((param) => param === 'sys_operator')), '操作日志用户操作人筛选应使用 actor_system_account_id 精确条件')
  for (const call of keywordSearchCalls) {
    const plan = explainQueryPlan(datasetDatabase, call.sql, call.params)
    assertPlanUses(plan, 'idx_operation_log_summary_search_terms_term_created', '操作日志摘要搜索必须由 term + created_at 索引驱动')
    if (!/\bUNION\s+ALL\b/i.test(call.sql)) {
      assertNoTempBtree(plan, '操作日志管理员关键词列表不应为排序建立临时 B-tree')
    }
  }
  assert(capturedCalls.some((call) => /\bal\.client_ip\s+>=\s+\?/i.test(call.sql)
    && /\bal\.client_ip\s+<\s+\?/i.test(call.sql)), '审计 clientIp 前缀检索应使用范围条件而不是 LIKE')
  const auditLogListQuerySource = readFileSync(resolve('src/storage/audit-log-list-query.ts'), 'utf8')
  const operationLogReadSource = readFileSync(resolve('src/storage/operation-log-read.repository.ts'), 'utf8')
  assert.match(auditLogListQuerySource, /runtimeConfig\.databaseDriver === 'postgres' \? `\$\{column\} COLLATE "C"` : column/, 'PG 审计 traceId/clientIp 前缀筛选必须使用 C collation')
  assert.match(auditLogListQuerySource, /textPrefixUpperBound\(text\)/, '审计 traceId/clientIp 前缀筛选必须使用统一二进制上界')
  assert.match(auditLogListQuerySource, /al\.created_at >= \?[\s\S]*al\.created_at <= \?/, '审计普通列表必须绑定 created_at 时间窗口筛选')
  assert.match(operationLogReadSource, /runtimeConfig\.databaseDriver === 'postgres' \? `\$\{column\} COLLATE "C"` : column/, 'PG 操作日志 traceId 前缀筛选必须使用 C collation')
  assert.match(operationLogReadSource, /textPrefixUpperBound\(text\)/, '操作日志 traceId 前缀筛选必须使用统一二进制上界')

  const boundedCalls: Array<{ sql: string; params: unknown[] }> = []
  const boundedDatasetDatabase = databaseModule.getDatasetDatabase()
  const boundedOriginalPrepare = boundedDatasetDatabase.prepare.bind(boundedDatasetDatabase) as typeof boundedDatasetDatabase.prepare
  boundedDatasetDatabase.prepare = ((sql: string) => {
    const statement = boundedOriginalPrepare(sql)
    if (/\bFROM\s+operation_logs\s+ol\b/i.test(sql) || /\bFROM\s+operation_log_summary_search_terms\s+search\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        boundedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof boundedDatasetDatabase.prepare
  try {
    const shortKeywordWithWindow = repositories.listOperationLogs({
      summaryKeyword: '造数',
      startAt: '2026-02-01T00:00:00.000Z',
      endAt: '2026-02-01T01:00:00.000Z',
      pageSize: 10
    })
    assert.deepEqual(
      shortKeywordWithWindow.items.map((item) => item.id),
      ['op_log_guard_short_keyword_middle', 'op_log_guard_short_keyword'],
      '短中文摘要搜索带时间窗时仍通过摘要倒排词项匹配'
    )

    const viewerShortKeywordWithWindow = repositories.listOperationLogsForViewer('sys_user', {
      summaryKeyword: '造数',
      startAt: '2026-02-01T00:00:00.000Z',
      endAt: '2026-02-01T01:00:00.000Z',
      pageSize: 10
    })
    assert.deepEqual(
      viewerShortKeywordWithWindow.items.map((item) => item.id),
      ['op_log_guard_short_keyword_middle', 'op_log_guard_short_keyword'],
      '用户侧短中文摘要搜索也应通过摘要倒排词项并保留可见性过滤'
    )
  } finally {
    boundedDatasetDatabase.prepare = boundedOriginalPrepare
  }
  assert(boundedCalls.some((call) => /\boperation_log_summary_search_terms\s+search\b/i.test(call.sql)
    && /\bsearch\.term\s*=\s*\?/i.test(call.sql)
    && /\bol\.created_at\s+>=\s+\?/i.test(call.sql)
    && /\bol\.created_at\s+<=\s+\?/i.test(call.sql)
    && call.params.some((param) => param === '造数')), '短中文摘要搜索带时间窗时应同时绑定时间条件和摘要倒排词项')
  for (const call of boundedCalls) {
    assert(!/\bMATCH\s+\?/i.test(call.sql), '小时间窗操作日志摘要搜索也不应使用 MATCH 查询')
  }
  assertManagementLogRoutesUseBoundedTimeouts()

  console.log('日志列表查询防护回归通过：审计结构化过滤无前导通配符，操作日志摘要搜索使用倒排词项索引，管理日志路由不使用无限超时')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
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

function assertManagementLogRoutesUseBoundedTimeouts(): void {
  const routeFiles = [
    resolve('src/modules/audit-logs/audit-logs.routes.ts'),
    resolve('src/modules/runtime-logs/runtime-logs.routes.ts')
  ]
  for (const routeFile of routeFiles) {
    const source = readFileSync(routeFile, 'utf8')
    assert.doesNotMatch(source, /setTimeout\(\s*0\s*\)/, `${routeFile} 不应使用无限请求超时`)
    assert.match(source, /const\s+\w+RouteTimeoutMs\s*=\s*120_000/, `${routeFile} 应声明有限管理路由超时`)
    assert.match(source, /req\.setTimeout\(\w+RouteTimeoutMs\)/, `${routeFile} 请求超时应绑定命名常量`)
    assert.match(source, /res\.setTimeout\(\w+RouteTimeoutMs\)/, `${routeFile} 响应超时应绑定命名常量`)
  }
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
    providerCode: 'gpt',
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
  actorSystemAccountId?: string
  actorUsername?: string
  actorDisplayName: string
  createdAt: string
  viewers: OperationLogInput['viewers']
}): OperationLogInput {
  return {
    id: input.id,
    traceId: input.traceId,
    actorSystemAccountId: input.actorSystemAccountId ?? 'sys_admin',
    actorUsername: input.actorUsername ?? 'admin',
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
