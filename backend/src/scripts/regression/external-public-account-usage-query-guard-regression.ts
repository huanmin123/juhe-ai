import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { DatabaseSync, SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import type { PublicAccountUsageSortField } from '../../modules/external-integrations/external-public-welfare.service.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-external-public-account-usage-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'external-public-account-usage-query-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, usageStatsHelpers, publicWelfareService] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-stats-helpers.js'),
  import('../../modules/external-integrations/external-public-welfare.service.js')
])

try {
  const database = databaseModule.getStatsDatabase()
  const today = usageStatsHelpers.dateKey(new Date(), usageStatsHelpers.usageStatsTimezone())
  seedUsageWindow(database, today)

  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: SQLInputValue[] }> = []
  database.prepare = ((sql: string) => {
    if (/\bFROM\s+usage_stats_daily\b/i.test(sql)) {
      throw new Error('公开账号用量接口不应在请求链路读取 usage_stats_daily')
    }
    if (/\bGROUP\s+BY\s+scope_id\b/i.test(sql)) {
      throw new Error('公开账号用量接口不应在请求链路按账号聚合')
    }
    const statement = originalPrepare(sql)
    const originalAll = statement.all.bind(statement) as typeof statement.all
    statement.all = ((...params: SQLInputValue[]) => {
      if (/\bFROM\s+usage_scope_range_windows\s+usage_window\b/i.test(sql)) {
        capturedCalls.push({ sql, params })
      }
      return originalAll(...params)
    }) as typeof statement.all
    return statement
  }) as typeof database.prepare

  try {
    const sortIndexByField = new Map<PublicAccountUsageSortField, string>([
      ['requestCount', 'idx_usage_scope_range_windows_request_count'],
      ['successCount', 'idx_usage_scope_range_windows_success_count'],
      ['errorCount', 'idx_usage_scope_range_windows_error_count'],
      ['errorRate', 'idx_usage_scope_range_windows_error_rate'],
      ['totalTokens', 'idx_usage_scope_range_windows_total_tokens'],
      ['totalCost', 'idx_usage_scope_range_windows_total_cost'],
      ['activeDays', 'idx_usage_scope_range_windows_active_days'],
      ['lastUsedAt', 'idx_usage_scope_range_windows_last_used']
    ])

    for (const [sortField, expectedIndex] of sortIndexByField) {
      const result = publicWelfareService.getPublicAccountUsage({
        range: 'today',
        page: 1,
        pageSize: 2,
        sortField,
        sortOrder: 'desc'
      })
      assert.equal(result.rangeReady, true, `${sortField} 排序应命中已生成的窗口`)
      assert(result.items.length > 0, `${sortField} 排序应返回窗口数据`)
      assertPublicAccountUsageItemsDoNotExposeOwner(result.items)
      const call = capturedCalls.pop()
      assert(call, `${sortField} 排序应读取账号用量窗口`)
      const plan = explainQueryPlan(database, call.sql, call.params)
      assertNoTempBtree(plan, `${sortField} 排序`)
      assert(plan.includes(expectedIndex), `${sortField} 排序应使用 ${expectedIndex}，实际计划：${plan}`)
    }

    const deepPageResult = publicWelfareService.getPublicAccountUsage({
      range: 'today',
      page: 1000,
      pageSize: 100,
      sortField: 'requestCount',
      sortOrder: 'desc'
    })
    assert.equal(deepPageResult.page, 10, '公开账号用量深页码应收敛到 1001 行窗口内')
    const deepPageCall = capturedCalls.pop()
    assert(deepPageCall, '公开账号用量深页码仍应执行窗口查询')
    const deepPageOffset = Number(deepPageCall.params.at(-1) ?? NaN)
    assert(Number.isFinite(deepPageOffset) && deepPageOffset <= 1000, `公开账号用量 SQL OFFSET 不应超过 1000，实际：${deepPageOffset}`)
  } finally {
    database.prepare = originalPrepare
  }

  const businessDatabase = databaseModule.getBusinessDatabase()
  const originalBusinessPrepare = businessDatabase.prepare.bind(businessDatabase) as typeof businessDatabase.prepare
  const accountLookupCalls: Array<{ sql: string; params: SQLInputValue[] }> = []
  businessDatabase.prepare = ((sql: string) => {
    const statement = originalBusinessPrepare(sql)
    if (/\bFROM\s+accounts\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        if (/\bWHERE\s+(?:id|name|provider_code|type)\b/i.test(sql) || /\bINDEXED\s+BY\s+idx_accounts_/i.test(sql)) {
          accountLookupCalls.push({ sql, params })
        }
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof businessDatabase.prepare

  try {
    const keywordResult = publicWelfareService.getPublicAccountUsage({
      range: 'today',
      keyword: 'open',
      page: 1,
      pageSize: 2
    })
    assert.equal(keywordResult.rangeReady, true, '关键词查询仍应读取已生成窗口状态')
  } finally {
    businessDatabase.prepare = originalBusinessPrepare
  }

  assert(accountLookupCalls.length >= 4, '公开账号用量关键词应拆分为多个索引化账号 ID 查询')
  for (const call of accountLookupCalls) {
    assert(!/\bLIKE\b/i.test(call.sql), `公开账号用量关键词解析不应使用 LIKE：${call.sql}`)
    assert(!/\bOR\b/i.test(call.sql), `公开账号用量关键词解析不应使用 OR 合并多列：${call.sql}`)
    const plan = explainQueryPlan(businessDatabase, call.sql, call.params)
    assertNoTempBtree(plan, '公开账号用量关键词账号解析')
    if (/\bidx_accounts_name_lookup\b/i.test(call.sql)) {
      assert(plan.includes('idx_accounts_name_lookup'), `账号名称关键词解析应使用 idx_accounts_name_lookup，实际计划：${plan}`)
    }
    if (/\bidx_accounts_provider_lookup\b/i.test(call.sql)) {
      assert(plan.includes('idx_accounts_provider_lookup'), `账号供应商关键词解析应使用 idx_accounts_provider_lookup，实际计划：${plan}`)
    }
    if (/\bidx_accounts_type_lookup\b/i.test(call.sql)) {
      assert(plan.includes('idx_accounts_type_lookup'), `账号类型关键词解析应使用 idx_accounts_type_lookup，实际计划：${plan}`)
    }
  }

  console.log('公开账号用量查询防护回归通过：请求链路只读范围窗口表，各排序入口不再扫描日表或创建临时排序树')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedUsageWindow(database: DatabaseSync, today: string): void {
  const now = new Date().toISOString()
  const insert = database.prepare(`
    INSERT INTO usage_scope_range_windows (
      system_account_id, scope_type, scope_id, start_date, end_date,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      cache_read_cost_usd, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max, active_days,
      last_used_at, last_error_at, updated_at
    ) VALUES ('global', 'account', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  insert.run(
    'acc_public_usage_query_guard_hot',
    today,
    today,
    30,
    28,
    2,
    600,
    500,
    60,
    0.01,
    0.12,
    3000,
    30,
    260,
    900,
    30,
    80,
    1,
    now,
    now,
    now
  )
  insert.run(
    'acc_public_usage_query_guard_cold',
    today,
    today,
    2,
    1,
    1,
    10,
    5,
    0,
    0,
    0.001,
    100,
    2,
    60,
    40,
    2,
    20,
    1,
    now,
    now,
    now
  )
}

function explainQueryPlan(database: DatabaseSync, sql: string, params: SQLInputValue[]): string {
  const rows = database
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params) as Array<{ detail?: string }>
  return rows.map((row) => row.detail ?? '').filter(Boolean).join('\n')
}

function assertNoTempBtree(details: string, label: string): void {
  assert(!/USE TEMP B-TREE/i.test(details), `${label}不应创建临时排序树，实际计划：${details}`)
}

function assertPublicAccountUsageItemsDoNotExposeOwner(items: ReadonlyArray<object>): void {
  assert(items.length > 0, '公开账号用量应返回可验证的测试数据')
  for (const item of items) {
    assert(!Object.prototype.hasOwnProperty.call(item, 'ownerSystemAccountId'), '公开账号用量不应返回内部系统账户 ID')
    assert(!Object.prototype.hasOwnProperty.call(item, 'ownerSystemAccountName'), '公开账号用量不应返回内部系统账户名称')
  }
}
