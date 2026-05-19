import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-ai-performance-account-options-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'ai-performance-account-options-query-guard-secret'
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
  const owner = repositories.createSystemAccount({
    username: 'perf-owner',
    displayName: '性能账号用户',
    password: 'Password-123456',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const matchedAccount = repositories.createAccount({
    providerCode: 'openai',
    name: 'perfneedle 主账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ai-performance-options-query-guard-matched',
      base_url: 'https://api.openai.com/v1'
    }
  }, ownerAccess)
  const otherOwnerAccount = repositories.createAccount({
    providerCode: 'openai',
    name: 'otherneedle 主账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ai-performance-options-query-guard-other',
      base_url: 'https://api.openai.com/v1'
    }
  }, ownerAccess)
  const adminAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '管理员普通账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ai-performance-options-query-guard-admin',
      base_url: 'https://api.openai.com/v1'
    }
  }, adminAccess)

  const database = databaseModule.getDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const shouldCapture = /^\s*SELECT\s+accounts\.id\s+FROM\s+accounts\b/i.test(sql)
      || /^\s*SELECT\s+id\s+FROM\s+system_accounts\b/i.test(sql)
    if (shouldCapture) {
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
  }) as typeof database.prepare

  try {
    const nameKeyword = usageStatsRepository.listAiPerformanceAccountOptions(adminAccess, {
      keyword: 'perfneedle',
      limit: 10
    })
    assert.deepEqual(nameKeyword.map((account) => account.id), [matchedAccount.id], 'AI 性能账号选项应支持账号名前缀搜索')

    const idPrefix = usageStatsRepository.listAiPerformanceAccountOptions(adminAccess, {
      keyword: uniquePrefix(matchedAccount.id, otherOwnerAccount.id),
      limit: 10
    })
    assert.deepEqual(idPrefix.map((account) => account.id), [matchedAccount.id], 'AI 性能账号选项应支持账号 ID 前缀搜索')

    const ownerKeyword = usageStatsRepository.listAiPerformanceAccountOptions(adminAccess, {
      keyword: owner.displayName,
      limit: 10
    })
    assert(ownerKeyword.some((account) => account.id === matchedAccount.id), '管理员全局视图应支持按系统账号名前缀定位账号')

    const userScopedKeyword = usageStatsRepository.listAiPerformanceAccountOptions(ownerAccess, {
      keyword: '管理员',
      limit: 10
    })
    assert(!userScopedKeyword.some((account) => account.id === adminAccount.id), '用户侧 AI 性能账号选项不能因关键词命中其他 owner 账号而越权')
  } finally {
    database.prepare = originalPrepare
  }

  const accountOptionCalls = capturedCalls.filter((call) => /^\s*SELECT\s+accounts\.id\s+FROM\s+accounts\b/i.test(call.sql))
  assert(accountOptionCalls.length >= 4, '回归应捕获 AI 性能账号选项账号查询')
  for (const call of capturedCalls) {
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), 'AI 性能账号选项查询不应传入前导通配符参数')
  }
  for (const call of accountOptionCalls) {
    assert(!/\bLEFT\s+JOIN\s+system_accounts\b/i.test(call.sql), 'AI 性能账号选项关键词查询不应为 owner 名称挂系统账号表')
    assert(!/\bCOALESCE\s*\(\s*system_accounts\./i.test(call.sql), 'AI 性能账号选项关键词查询不应在账号查询中扫描系统账号展示字段')
  }
  assertBusinessIndexExists('idx_accounts_name_lookup')
  assertBusinessIndexExists('idx_accounts_system_account_name_lookup')
  assertBusinessIndexExists('idx_system_accounts_display_name_lookup')

  console.log('AI 性能账号选项查询防护回归通过：关键词使用精确/前缀条件，owner 名称先解析 ID，不再传入前导通配符')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function uniquePrefix(value: string, otherValue: string): string {
  for (let length = 1; length <= value.length; length += 1) {
    const prefix = value.slice(0, length)
    if (!otherValue.startsWith(prefix)) return prefix
  }
  return value
}

function assertBusinessIndexExists(indexName: string): void {
  const row = databaseModule.getDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `业务库应创建索引 ${indexName}`)
}
