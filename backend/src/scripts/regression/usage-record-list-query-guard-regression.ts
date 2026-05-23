import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-record-list-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'usage-record-list-query-guard-secret'
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
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: '使用记录查询防护分组',
    providerCode: 'openai',
    enabled: true
  }, access)
  const otherGroup = repositories.createGroup({
    name: '使用记录查询防护其他分组',
    providerCode: 'openai',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '使用记录查询防护账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-list-query-guard',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const middleNameAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '普通使用记录查询防护账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-list-query-guard-middle',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const otherGroupAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '其他分组账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-list-query-guard-other-group',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: otherGroup.id
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '使用记录查询防护 Key',
    groupId: group.id
  }, access)
  const otherApiKey = repositories.createApiKeyRecord({
    name: '使用记录查询防护其他 Key',
    groupId: otherGroup.id
  }, access)
  repositories.createUsageRecordsBatch([
    {
      id: 'usage_list_query_guard_exact',
      traceId: 'trace-usage-list-query-guard-exact',
      apiKeyId: apiKey.id,
      groupId: group.id,
      accountId: account.id,
      endpoint: '/v1/responses',
      providerCode: 'openai',
      model: 'gpt-5.5',
      stream: false,
      statusCode: 200,
      success: true,
      createdAt: '2026-01-02T00:00:00.000Z'
    },
    {
      id: 'usage_list_query_guard_prefix_only',
      traceId: 'trace-usage-list-query-guard-prefix-only',
      apiKeyId: apiKey.id,
      groupId: group.id,
      accountId: account.id,
      endpoint: '/v1/responses',
      providerCode: 'openai',
      model: 'gpt-5.5-mini',
      stream: false,
      statusCode: 200,
      success: true,
      createdAt: '2026-01-02T00:00:01.000Z'
    },
    {
      id: 'usage_list_query_guard_middle_name',
      traceId: 'trace-usage-list-query-guard-middle-name',
      apiKeyId: apiKey.id,
      groupId: group.id,
      accountId: middleNameAccount.id,
      endpoint: '/v1/responses',
      providerCode: 'openai',
      model: 'gpt-4.1',
      stream: false,
      statusCode: 200,
      success: true,
      createdAt: '2026-01-02T00:00:02.000Z'
    },
    {
      id: 'usage_list_query_guard_other_group',
      traceId: 'trace-usage-list-query-guard-other-group',
      apiKeyId: otherApiKey.id,
      groupId: otherGroup.id,
      accountId: otherGroupAccount.id,
      endpoint: '/v1/responses',
      providerCode: 'openai',
      model: 'gpt-5.5-other-group',
      stream: false,
      statusCode: 200,
      success: true,
      createdAt: '2026-01-02T00:00:03.000Z'
    }
  ])

  const businessDatabase = databaseModule.getDatabase()
  const originalBusinessPrepare = businessDatabase.prepare.bind(businessDatabase) as typeof businessDatabase.prepare
  const accountLookupCalls: Array<{ sql: string; params: unknown[] }> = []
  businessDatabase.prepare = ((sql: string) => {
    const statement = originalBusinessPrepare(sql)
    if (/^\s*SELECT\s+(?:accounts\.)?id\s+FROM\s+accounts\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        accountLookupCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof businessDatabase.prepare

  const recordDatabase = databaseModule.getDatasetDatabase()
  const originalPrepare = recordDatabase.prepare.bind(recordDatabase) as typeof recordDatabase.prepare
  const usageRecordListCalls: Array<{ sql: string; params: unknown[] }> = []
  recordDatabase.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bFROM\s+usage_records\s+ur\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        usageRecordListCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof recordDatabase.prepare

  try {
    const exactModel = repositories.listUsageRecords(access, { model: 'gpt-5.5', page: 1, pageSize: 10 })
    assert.deepEqual(exactModel.items.map((item) => item.id), ['usage_list_query_guard_exact'], 'model 筛选应按精确值匹配，不应把前缀模型一并查出')

    const accountNamePrefix = repositories.listUsageRecords(access, { accountKeyword: '使用记录查询防护', page: 1, pageSize: 10 })
    assert.deepEqual(accountNamePrefix.items.map((item) => item.id), ['usage_list_query_guard_prefix_only', 'usage_list_query_guard_exact'], '账号名称关键字应按前缀匹配，不应命中中间包含名称')

    const accountPrefix = repositories.listUsageRecords(access, { accountKeyword: uniquePrefix(account.id, middleNameAccount.id), page: 1, pageSize: 10 })
    assert.equal(accountPrefix.items.length, 0, '账号名称关键字不应支持账号 ID 前缀定位使用记录')

    const groupFiltered = repositories.listUsageRecords(access, { groupId: group.id, page: 1, pageSize: 10 })
    assert.deepEqual(groupFiltered.items.map((item) => item.id), ['usage_list_query_guard_middle_name', 'usage_list_query_guard_prefix_only', 'usage_list_query_guard_exact'], '分组筛选应只返回目标分组的使用记录')
  } finally {
    recordDatabase.prepare = originalPrepare
    businessDatabase.prepare = originalBusinessPrepare
  }

  assert(accountLookupCalls.length >= 2, '回归应捕获账号关键词预解析 SQL')
  for (const call of accountLookupCalls) {
    assert(/\bESCAPE\s+'\\'/i.test(call.sql), '账号关键词预解析应显式转义 LIKE 通配符')
    assert(!/\bWHERE[\s\S]*\bid\s+(?:=|LIKE)\s+\?/i.test(call.sql), '账号关键词预解析不应把账号 ID 放进名称搜索 WHERE')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '账号关键词预解析不应传入前导通配符参数')
  }
  assert(usageRecordListCalls.length >= 2, '回归应捕获使用记录列表 SQL')
  for (const call of usageRecordListCalls) {
    assert(!/\bur\.model\s+LIKE\s+\?/i.test(call.sql), 'model 筛选不应在 usage_records 上使用 LIKE')
    assert(!/\bur\.account_id\s+(?:=|LIKE)\s+\?/i.test(call.sql), '使用记录账号名称搜索不应直接按 account_id 精确或前缀匹配')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '使用记录列表不应向大表筛选传入前导通配符参数')
  }
  assertQueryPlanUsesIndex(`
    SELECT id
    FROM usage_records ur
    WHERE ur.model = ?
    ORDER BY ur.created_at DESC, ur.id DESC
    LIMIT ?
  `, ['gpt-5.5', 10], 'idx_usage_records_model_created_sort')
  assertQueryPlanUsesIndex(`
    SELECT id
    FROM usage_records ur
    WHERE ur.system_account_id = ? AND ur.model = ?
    ORDER BY ur.created_at DESC, ur.id DESC
    LIMIT ?
  `, ['sys_admin', 'gpt-5.5', 10], 'idx_usage_records_system_account_model_created_sort')
  assertQueryPlanUsesIndex(`
    SELECT id
    FROM usage_records ur
    WHERE ur.group_id = ?
    ORDER BY ur.created_at DESC, ur.id DESC
    LIMIT ?
  `, [group.id, 10], 'idx_usage_records_group_created_sort')
  assertQueryPlanUsesIndex(`
    SELECT id
    FROM usage_records ur
    WHERE ur.system_account_id = ? AND ur.group_id = ?
    ORDER BY ur.created_at DESC, ur.id DESC
    LIMIT ?
  `, ['sys_admin', group.id, 10], 'idx_usage_records_system_account_group_created_sort')

  repositories.createUsageRecordsBatch([
    {
      id: 'usage_recent_shape_compact',
      traceId: 'trace-usage-recent-shape-compact',
      apiKeyId: apiKey.id,
      groupId: group.id,
      accountId: account.id,
      endpoint: 'POST /v1/responses/compact',
      providerCode: 'openai',
      model: 'gpt-5.5',
      stream: true,
      statusCode: 200,
      success: true,
      createdAt: '2026-01-02T00:00:03.000Z'
    },
    {
      id: 'usage_recent_shape_middle_endpoint',
      traceId: 'trace-usage-recent-shape-middle-endpoint',
      apiKeyId: apiKey.id,
      groupId: group.id,
      accountId: account.id,
      endpoint: 'POST /proxy/v1/responses',
      providerCode: 'openai',
      model: 'gpt-5.5',
      stream: true,
      statusCode: 200,
      success: true,
      createdAt: '2026-01-02T00:00:04.000Z'
    }
  ])

  const recentShapeCalls: Array<{ sql: string; params: unknown[] }> = []
  recordDatabase.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bSELECT\s+endpoint,\s+model,\s+stream,\s+created_at\b/i.test(sql) && /\bFROM\s+usage_records\b/i.test(sql)) {
      const originalGet = statement.get.bind(statement) as typeof statement.get
      statement.get = ((...params: SQLInputValue[]) => {
        recentShapeCalls.push({ sql, params })
        return originalGet(...params)
      }) as typeof statement.get
    }
    return statement
  }) as typeof recordDatabase.prepare
  try {
    const shape = repositories.findRecentOpenAIRequestShapeForAccount(account.id, group.id)
    assert.equal(shape?.endpoint, 'POST /v1/responses/compact', '最近 OpenAI 请求形态应按 endpoint 精确或子路径前缀识别，不应命中中间包含路径')
  } finally {
    recordDatabase.prepare = originalPrepare
  }
  assert(recentShapeCalls.length >= 1, '回归应捕获最近请求形态 SQL')
  for (const call of recentShapeCalls) {
    assert(!/\bLOWER\s*\(\s*endpoint\s*\)\s+LIKE\s+'%/i.test(call.sql), '最近请求形态不应使用 endpoint 前导通配符 LIKE')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '最近请求形态不应传入前导通配符参数')
  }

  console.log('使用记录列表查询防护回归通过：model 精确匹配，accountKeyword 和最近请求形态都不再对 usage_records 做前导通配符扫描')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertQueryPlanUsesIndex(sql: string, params: SQLInputValue[], indexName: string): void {
  const details = databaseModule.getDatasetDatabase()
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert(details.includes(indexName), `查询计划应使用 ${indexName}，实际计划：${details}`)
}

function uniquePrefix(value: string, otherValue: string): string {
  for (let length = 1; length <= value.length; length += 1) {
    const prefix = value.slice(0, length)
    if (!otherValue.startsWith(prefix)) return prefix
  }
  return value
}
