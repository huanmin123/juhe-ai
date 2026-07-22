import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { DatabaseSync, SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-ai-performance-hourly-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'ai-performance-hourly-query-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, usageStatsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js')
])

try {
  const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: 'AI 性能小时查询分组',
    providerCode: 'gpt',
    enabled: true
  }, adminAccess)
  const accountA = repositories.createAccount({
    providerCode: 'gpt',
    name: 'AI 性能小时查询 A',
    type: 'api_key',
    credentials: { api_key: 'sk-ai-performance-hourly-a', base_url: 'https://api.openai.com/v1' },
    groupId: group.id
  }, adminAccess)
  const accountB = repositories.createAccount({
    providerCode: 'gpt',
    name: 'AI 性能小时查询 B',
    type: 'api_key',
    credentials: { api_key: 'sk-ai-performance-hourly-b', base_url: 'https://api.openai.com/v1' },
    groupId: group.id
  }, adminAccess)
  seedHourlyRows(databaseModule.getStatsDatabase(), accountA.id, accountB.id)

  const database = databaseModule.getStatsDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: SQLInputValue[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bFROM\s+usage_stats_hourly\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare

  try {
    const overview = usageStatsRepository.getAiPerformanceOverview(adminAccess, {
      startDate: '2026-01-01',
      endDate: '2026-01-01',
      days: 1,
      maxDays: 31
    }, [accountA.id, accountB.id])
    assert.equal(overview.accounts.length, 2, 'AI 性能概览应返回手动选择账号')
    const hourlyCall = capturedCalls.find((call) => /\bscope_id\s+IN\s*\(/i.test(call.sql))
    assert(hourlyCall, 'AI 性能概览应按选中账号读取小时趋势')
    assert(!/\bORDER\s+BY\b/i.test(hourlyCall.sql), 'AI 性能小时趋势不应为了排序改走范围内全账号扫描')
    const plan = explainQueryPlan(database, hourlyCall.sql, hourlyCall.params)
    assertNoTempBtree(plan, 'AI 性能小时趋势查询')
    assert(plan.includes('idx_usage_stats_hourly_scope_hour'), `AI 性能小时趋势应使用账号维度小时索引，实际计划：${plan}`)
  } finally {
    database.prepare = originalPrepare
  }

  console.log('AI 性能小时趋势查询防护回归通过：选中账号趋势按账号小时索引读取，不再为了 ORDER BY 扫范围内全部账号')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedHourlyRows(database: DatabaseSync, accountAId: string, accountBId: string): void {
  const insert = database.prepare(`
    INSERT INTO usage_stats_hourly (
      system_account_id, scope_type, scope_id, stat_hour,
      request_count, success_count, input_tokens, output_tokens,
      duration_ms_sum, duration_ms_count, duration_ms_max,
      first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      last_used_at, updated_at
    ) VALUES ('global', 'account', ?, ?, ?, ?, 0, 0, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const [accountId, count] of [[accountAId, 3], [accountBId, 5]] as const) {
    insert.run(
      accountId,
      '2026-01-01T00',
      count,
      count,
      count * 120,
      count,
      150,
      count * 40,
      count,
      80,
      '2026-01-01T00:30:00.000Z',
      '2026-01-01T00:30:00.000Z'
    )
  }
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
