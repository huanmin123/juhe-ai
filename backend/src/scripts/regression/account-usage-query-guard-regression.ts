import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from '../../storage/usage-stats-types.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-usage-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-usage-query-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const range = {
  startDate: '2026-02-01',
  endDate: '2026-02-28',
  days: 28,
  maxDays: 31
}

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: '账号用量查询防护分组',
    providerCode: 'openai',
    enabled: true
  }, access)
  const matchedAccount = repositories.createAccount({
    providerCode: 'openai',
    name: 'keywordneedle 账号用量账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-usage-query-guard-matched',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const otherAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '普通 keywordneedle 账号用量账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-usage-query-guard-other',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const selectedAccount = repositories.createAccount({
    providerCode: 'openai',
    name: 'selected-account 账号用量账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-usage-query-guard-selected',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const notesOnlyAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '备注字段账号用量账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-usage-query-guard-notes',
      base_url: 'https://api.openai.com/v1'
    },
    notes: 'keywordnote 备注前缀',
    groupId: group.id
  }, access)

  seedUsageScopeRangeWindow(GLOBAL_STATS_SYSTEM_ACCOUNT_ID, 'account', matchedAccount.id, 7)
  seedUsageScopeRangeWindow(GLOBAL_STATS_SYSTEM_ACCOUNT_ID, 'account', otherAccount.id, 3)
  seedUsageScopeRangeWindow(GLOBAL_STATS_SYSTEM_ACCOUNT_ID, 'account', selectedAccount.id, 1)

  const businessDatabase = databaseModule.getDatabase()
  const originalBusinessPrepare = businessDatabase.prepare.bind(businessDatabase) as typeof businessDatabase.prepare
  const businessCalls: Array<{ sql: string; params: unknown[] }> = []
  businessDatabase.prepare = ((sql: string) => {
    const statement = originalBusinessPrepare(sql)
    if (/^\s*SELECT\s+accounts\.id\s+FROM\s+accounts\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        businessCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof businessDatabase.prepare

  const recordDatabase = databaseModule.getStatsDatabase()
  const originalPrepare = recordDatabase.prepare.bind(recordDatabase) as typeof recordDatabase.prepare
  const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
  recordDatabase.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bFROM\s+usage_scope_range_windows\s+usage_window\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      const originalGet = statement.get.bind(statement) as typeof statement.get
      statement.all = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
      statement.get = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalGet(...params)
      }) as typeof statement.get
    }
    return statement
  }) as typeof recordDatabase.prepare

  try {
    const keywordResult = repositories.getAccountUsageStatsOverviewPage(access, {
      keyword: 'keywordneedle',
      page: 1,
      pageSize: 10,
      range
    })
    assert.deepEqual(keywordResult.rows.map((row) => row.id), [matchedAccount.id], '账号用量关键词应先解析成账号 ID 后再筛统计窗口')
    assert.equal(keywordResult.rows[0]?.rangeUsage.requestCount, 7, '账号用量关键词结果应保留范围窗口用量')

    const prefixResult = repositories.getAccountUsageStatsOverviewPage(access, {
      keyword: uniquePrefix(matchedAccount.id, otherAccount.id),
      page: 1,
      pageSize: 10,
      range
    })
    assert.deepEqual(prefixResult.rows.map((row) => row.id), [matchedAccount.id], '账号用量关键词仍应支持账号 ID 前缀定位')

    const missResult = repositories.getAccountUsageStatsOverviewPage(access, {
      keyword: 'missing-keyword-needle',
      page: 1,
      pageSize: 10,
      range
    })
    assert.equal(missResult.total, 0, '账号用量关键词无匹配账号时应直接返回空窗口')

    const notesOnlyResult = repositories.getAccountUsageStatsOverviewPage(access, {
      keyword: 'keywordnote',
      page: 1,
      pageSize: 10,
      range
    })
    assert.equal(notesOnlyResult.total, 0, '账号用量关键词不应通过备注字段命中账号，避免通用关键词扫描长文本')
    assert(!notesOnlyResult.rows.some((row) => row.id === notesOnlyAccount.id), '备注字段命中的账号不应混入账号用量结果')

    const selectedResult = repositories.getAccountUsageStatsOverviewPage(access, {
      accountIds: [selectedAccount.id],
      page: 1,
      pageSize: 1,
      range
    })
    assert.deepEqual(selectedResult.rows.map((row) => row.id), [matchedAccount.id, selectedAccount.id], '账号用量手动选择的账户应按 ID 补入当前页结果')
    assert.equal(selectedResult.rows[1]?.rangeUsage.requestCount, 1, '账号用量手动选择补入行应读取预聚合范围窗口')
    assert.equal(selectedResult.hasMore, true, '账号用量手动补入不应抹掉原始分页 hasMore')

    const selectedPageTwoResult = repositories.getAccountUsageStatsOverviewPage(access, {
      accountIds: [selectedAccount.id],
      page: 2,
      pageSize: 1,
      range
    })
    assert.deepEqual(selectedPageTwoResult.rows.map((row) => row.id), [otherAccount.id, selectedAccount.id], '账号用量翻页后仍应把手动选择账户补入当前页结果')
    assert.equal(selectedPageTwoResult.total, 3, '账号用量手动补入行不应让分页上界 total 低估总结果数')

    const typeIgnoredResult = repositories.getAccountUsageStatsOverviewPage(access, {
      type: 'oauth',
      page: 1,
      pageSize: 10,
      range
    })
    assert.deepEqual(typeIgnoredResult.rows.map((row) => row.id), [matchedAccount.id, otherAccount.id, selectedAccount.id], '账号用量统计不应按 OAuth/API Key 账号类型缩窄明细')
  } finally {
    recordDatabase.prepare = originalPrepare
    businessDatabase.prepare = originalBusinessPrepare
  }

  assert(businessCalls.length >= 3, '回归应捕获账号用量关键词预解析 SQL')
  for (const call of businessCalls) {
    assert(!/\bCOALESCE\s*\(/i.test(call.sql), '账号用量关键词预解析不应通过 COALESCE 做包含扫描')
    assert(!/\baccounts\.notes\s+(?:COLLATE|LIKE)\b/i.test(call.sql), '账号用量关键词预解析不应把备注字段放进通用关键词 WHERE')
    assert(/\bESCAPE\s+'\\'/i.test(call.sql), '账号用量关键词预解析前缀搜索应显式转义 LIKE 通配符')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '账号用量关键词预解析不应接收前导通配符参数')
  }
  assert(capturedCalls.length >= 4, '回归应捕获账号用量窗口查询 SQL')
  assert(capturedCalls.some((call) => /\busage_window\.scope_id\s+IN\s*\(/i.test(call.sql)), '账号用量关键词窗口查询应使用 scope_id 命中预解析账号')
  assert(capturedCalls.some((call) => /\busage_window\.scope_id\s+IN\s*\(/i.test(call.sql) && call.params.includes(selectedAccount.id)), '账号用量手动选择补入应使用 scope_id 命中选中账号')
  assert(capturedCalls.some((call) => /\bAND\s+0\s+=\s+1\b/i.test(call.sql)), '账号用量关键词无匹配时应避免扫描窗口表')
  for (const call of capturedCalls) {
    assert(!/\bLIKE\s+\?/i.test(call.sql), '账号用量窗口查询不应拼入业务字段 LIKE')
    assert(!/\baccount_usage_business\.accounts\b/i.test(call.sql), '账号用量关键词窗口查询不应在记录库查询内挂业务库账号表')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '账号用量窗口查询不应接收前导通配符参数')
  }

  assertQueryPlanUsesIndex(`
    SELECT scope_id
    FROM usage_scope_range_windows usage_window
    WHERE usage_window.system_account_id = ?
      AND usage_window.scope_type = ?
      AND usage_window.start_date = ?
      AND usage_window.end_date = ?
      AND (
        usage_window.request_count > 0
        OR usage_window.input_tokens > 0
        OR usage_window.output_tokens > 0
        OR usage_window.cache_read_tokens > 0
        OR usage_window.total_cost_usd > 0
        OR usage_window.last_used_at IS NOT NULL
      )
    LIMIT ?
  `, [GLOBAL_STATS_SYSTEM_ACCOUNT_ID, 'account', range.startDate, range.endDate, 10], 'idx_usage_scope_range_windows_range_lookup')

  console.log('账号用量查询防护回归通过：关键词先解析账号 ID，手动选中账户按窗口 scope_id 补入，窗口查询不再接收前导通配符，并使用日期范围索引')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedUsageScopeRangeWindow(systemAccountId: string, scopeType: string, scopeId: string, requestCount: number): void {
  const updatedAt = '2026-02-28T23:59:59.000Z'
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO usage_scope_range_windows (
        system_account_id, scope_type, scope_id, start_date, end_date,
        request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
        total_cost_usd, last_used_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, 0, ?, ?, ?)
    `)
    .run(systemAccountId, scopeType, scopeId, range.startDate, range.endDate, requestCount, requestCount * 0.01, updatedAt, updatedAt)
}

function uniquePrefix(value: string, otherValue: string): string {
  for (let length = 1; length <= value.length; length += 1) {
    const prefix = value.slice(0, length)
    if (!otherValue.startsWith(prefix)) return prefix
  }
  return value
}

function assertQueryPlanUsesIndex(sql: string, params: SQLInputValue[], indexName: string): void {
  const details = databaseModule.getStatsDatabase()
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert(details.includes(indexName), `查询计划应使用 ${indexName}，实际计划：${details}`)
}
